package clientcore

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/getlantern/broflake/common"
)

// socketFreddie answers the signaling endpoint and, unless noSocket, the volunteer socket, each
// with reply. It counts POSTs and socket dials.
type socketFreddie struct {
	*httptest.Server
	noSocket bool
	reply    func(form url.Values) common.VolunteerReply
	posts    atomic.Int32
	dials    atomic.Int32
	mu       sync.Mutex
	sockets  []*websocket.Conn
}

func newSocketFreddie(t *testing.T, noSocket bool, reply func(form url.Values) common.VolunteerReply) *socketFreddie {
	f := &socketFreddie{noSocket: noSocket, reply: reply}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/signal", func(w http.ResponseWriter, r *http.Request) {
		f.posts.Add(1)
		require.NoError(t, r.ParseForm())
		res := f.reply(r.PostForm)
		w.WriteHeader(res.Status)
		w.Write([]byte(res.Body))
	})
	mux.HandleFunc("GET "+common.VolunteerSocketEndpoint, func(w http.ResponseWriter, r *http.Request) {
		if f.noSocket {
			http.NotFound(w, r)
			return
		}
		assert.Equal(t, common.Version, r.URL.Query().Get(common.VolunteerSocketVersionParam))
		f.dials.Add(1)
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.sockets = append(f.sockets, c)
		f.mu.Unlock()
		defer c.CloseNow()
		var wg sync.WaitGroup
		defer wg.Wait()
		for {
			_, b, err := c.Read(context.Background())
			if err != nil {
				return
			}
			var req common.VolunteerRequest
			require.NoError(t, json.Unmarshal(b, &req))
			wg.Add(1)
			go func() {
				defer wg.Done()
				res := f.reply(req.Form)
				res.ID = req.ID
				b, _ := json.Marshal(res)
				c.Write(context.Background(), websocket.MessageText, b)
			}()
		}
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *socketFreddie) options(socket bool) *WebRTCOptions {
	o := NewDefaultWebRTCOptions()
	o.DiscoverySrv = f.URL
	o.VolunteerSocket = socket
	return o
}

func echoData(form url.Values) common.VolunteerReply {
	return common.VolunteerReply{Status: http.StatusOK, Body: form.Get("data")}
}

func TestVolunteerSignaler_PostsWithoutTheOption(t *testing.T) {
	f := newSocketFreddie(t, false, echoData)
	s := newVolunteerSignaler(f.options(false))
	status, body, err := s.signal(context.Background(), url.Values{"data": {"x"}})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "x", string(body))
	assert.EqualValues(t, 1, f.posts.Load())
	assert.EqualValues(t, 0, f.dials.Load())
}

// Overlapping requests share one socket, each getting its own reply whatever order they come in.
func TestVolunteerSignaler_SharesOneSocket(t *testing.T) {
	f := newSocketFreddie(t, false, func(form url.Values) common.VolunteerReply {
		if form.Get("data") == "slow" {
			time.Sleep(300 * time.Millisecond)
		}
		return echoData(form)
	})
	s := newVolunteerSignaler(f.options(true))
	// Open the socket first, so the overlapping requests don't POST while one dials.
	_, _, err := s.signal(context.Background(), url.Values{"data": {"first"}})
	require.NoError(t, err)

	var wg sync.WaitGroup
	for _, data := range []string{"slow", "a", "b", "c"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, body, err := s.signal(context.Background(), url.Values{"data": {data}})
			assert.NoError(t, err)
			assert.Equal(t, http.StatusOK, status)
			assert.Equal(t, data, string(body))
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, f.dials.Load())
	assert.EqualValues(t, 0, f.posts.Load())
}

// A Freddie without the socket gets POSTs, and the producer doesn't dial again until the retry
// is due.
func TestVolunteerSignaler_FallsBackToPost(t *testing.T) {
	f := newSocketFreddie(t, true, echoData)
	s := newVolunteerSignaler(f.options(true))
	for range 3 {
		status, body, err := s.signal(context.Background(), url.Values{"data": {"x"}})
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, status)
		assert.Equal(t, "x", string(body))
	}
	assert.EqualValues(t, 3, f.posts.Load())
	s.mu.Lock()
	assert.True(t, s.retryAt.After(time.Now()))
	s.mu.Unlock()
}

// A request refused for the socket's in-flight limit goes as a POST.
func TestVolunteerSignaler_PostsWhenSocketIsFull(t *testing.T) {
	var fromSocket atomic.Bool
	fromSocket.Store(true)
	f := newSocketFreddie(t, false, func(form url.Values) common.VolunteerReply {
		if fromSocket.Swap(false) {
			return common.VolunteerReply{Status: http.StatusTooManyRequests}
		}
		return echoData(form)
	})
	s := newVolunteerSignaler(f.options(true))
	status, body, err := s.signal(context.Background(), url.Values{"data": {"x"}})
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "x", string(body))
	assert.EqualValues(t, 1, f.posts.Load())
}

// A request whose socket closes before its reply fails, rather than being sent again as a POST
// that Freddie might deliver twice, and the next request dials a new socket.
func TestVolunteerSignaler_SocketClosesMidRequest(t *testing.T) {
	var hang atomic.Bool
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	f := newSocketFreddie(t, false, func(form url.Values) common.VolunteerReply {
		if hang.Load() {
			<-release
		}
		return echoData(form)
	})
	s := newVolunteerSignaler(f.options(true))
	_, _, err := s.signal(context.Background(), url.Values{"data": {"x"}})
	require.NoError(t, err)

	hang.Store(true)
	done := make(chan error, 1)
	go func() {
		_, _, err := s.signal(context.Background(), url.Values{"data": {"y"}})
		done <- err
	}()
	time.Sleep(100 * time.Millisecond)
	f.mu.Lock()
	f.sockets[0].CloseNow()
	f.mu.Unlock()
	select {
	case err := <-done:
		assert.ErrorIs(t, err, errVolunteerSocketClosed)
	case <-time.After(5 * time.Second):
		t.Fatal("the request outlived its socket")
	}
	assert.EqualValues(t, 0, f.posts.Load())

	hang.Store(false)
	_, body, err := s.signal(context.Background(), url.Values{"data": {"z"}})
	require.NoError(t, err)
	assert.Equal(t, "z", string(body))
	assert.EqualValues(t, 2, f.dials.Load())
}

// The socket closes once nothing has used it for volunteerSocketIdle.
func TestVolunteerSignaler_ClosesWhenIdle(t *testing.T) {
	idle := volunteerSocketIdle
	volunteerSocketIdle = 100 * time.Millisecond
	t.Cleanup(func() { volunteerSocketIdle = idle })
	f := newSocketFreddie(t, false, echoData)
	s := newVolunteerSignaler(f.options(true))
	_, _, err := s.signal(context.Background(), url.Values{"data": {"x"}})
	require.NoError(t, err)
	s.mu.Lock()
	vc := s.conn
	s.mu.Unlock()
	require.NotNil(t, vc)
	require.Eventually(t, vc.isClosed, 2*time.Second, 10*time.Millisecond)
}
