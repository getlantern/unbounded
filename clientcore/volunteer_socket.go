package clientcore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/getlantern/broflake/common"
)

const (
	// volunteerSocketDialTimeout bounds dialing the volunteer socket.
	volunteerSocketDialTimeout = 10 * time.Second
	// volunteerSocketRetry is how long a producer POSTs after failing to dial the socket before it
	// dials again, so a Freddie or network path without the socket costs one dial now and then.
	volunteerSocketRetry = 5 * time.Minute
	// volunteerSocketWriteTimeout bounds sending one request. It isn't the worker's ctx, since
	// coder/websocket closes a connection whose write is cancelled, and the socket is shared.
	volunteerSocketWriteTimeout = 10 * time.Second
)

// volunteerSocketIdle is how long the socket stays open with nothing to send, so it closes once
// the producer stops.
var volunteerSocketIdle = time.Minute

var (
	// errVolunteerSocketClosed is the error for a request whose socket closed before its reply
	// came.
	errVolunteerSocketClosed = errors.New("volunteer socket closed")
	// errVolunteerSocketUnsent is the error for a request whose socket had closed before it was
	// sent, so it can go as a POST instead.
	errVolunteerSocketUnsent = errors.New("volunteer socket closed before sending")
)

// volunteerSignaler sends a producer's genesis and answer messages to Freddie. With
// WebRTCOptions.VolunteerSocket it sends them over one WebSocket that all of an instance's
// producer workers share (see common.VolunteerSocketEndpoint), rather than one POST each, and
// POSTs whenever it doesn't have the socket. Freddie handles both alike.
type volunteerSignaler struct {
	options *WebRTCOptions
	nextID  atomic.Uint64
	// dialing is held by the worker dialing the socket. The others POST in the meantime rather
	// than wait on the dial.
	dialing sync.Mutex
	mu      sync.Mutex
	conn    *volunteerConn
	retryAt time.Time
}

func newVolunteerSignaler(options *WebRTCOptions) *volunteerSignaler {
	return &volunteerSignaler{options: options}
}

// signal sends form to Freddie, as a POST to the signaling endpoint would, and returns the status
// and body of Freddie's reply. err means no reply came, or none could be read: status is then
// Freddie's if it got that far, and 0 otherwise.
func (s *volunteerSignaler) signal(ctx context.Context, form url.Values) (status int, body []byte, err error) {
	if s.options.VolunteerSocket {
		if vc := s.socket(ctx); vc != nil {
			status, body, err := vc.signal(ctx, s.nextID.Add(1), form)
			// Freddie refuses a request over the socket's limit of requests in flight, unseen, so
			// it can go as a POST.
			if !errors.Is(err, errVolunteerSocketUnsent) && (err != nil || status != http.StatusTooManyRequests) {
				return status, body, err
			}
		}
	}
	return postSignal(ctx, s.options, form)
}

// socket returns the open socket, dialing it if there's none, or nil if there's none to be had.
func (s *volunteerSignaler) socket(ctx context.Context) *volunteerConn {
	s.mu.Lock()
	vc, retryAt := s.conn, s.retryAt
	s.mu.Unlock()
	if vc != nil && !vc.isClosed() {
		return vc
	}
	if time.Now().Before(retryAt) || !s.dialing.TryLock() {
		return nil
	}
	defer s.dialing.Unlock()
	// Another worker may have dialed while this one checked.
	s.mu.Lock()
	vc = s.conn
	s.mu.Unlock()
	if vc != nil && !vc.isClosed() {
		return vc
	}

	dialCtx, cancel := context.WithTimeout(ctx, volunteerSocketDialTimeout)
	defer cancel()
	vc, err := dialVolunteerSocket(dialCtx, s.options)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		slog.Debug("Couldn't open the volunteer socket, POSTing instead", "error", err)
		s.conn, s.retryAt = nil, time.Now().Add(volunteerSocketRetry)
		return nil
	}
	s.conn = vc
	return vc
}

// volunteerSocketURL is the volunteer socket's URL on the Freddie at discoverySrv.
func volunteerSocketURL(discoverySrv string) string {
	u := strings.Replace(discoverySrv, "http://", "ws://", 1)
	u = strings.Replace(u, "https://", "wss://", 1)
	return u + common.VolunteerSocketEndpoint + "?" +
		url.Values{common.VolunteerSocketVersionParam: {common.Version}}.Encode()
}

func dialVolunteerSocket(ctx context.Context, options *WebRTCOptions) (*volunteerConn, error) {
	dialOpts := &websocket.DialOptions{}
	setEgressHTTPClient(dialOpts, options.HTTPClient)
	c, _, err := websocket.Dial(ctx, volunteerSocketURL(options.DiscoverySrv), dialOpts)
	if err != nil {
		return nil, err
	}
	vc := &volunteerConn{
		c:       c,
		closed:  make(chan struct{}),
		pending: map[uint64]chan common.VolunteerReply{},
	}
	vc.idleTimer = time.AfterFunc(volunteerSocketIdle, vc.closeIfIdle)
	go vc.read()
	return vc, nil
}

// volunteerConn is one open volunteer socket. Requests overlap on it, each waiting for the reply
// with its ID.
type volunteerConn struct {
	c         *websocket.Conn
	closed    chan struct{}
	closeOnce sync.Once
	mu        sync.Mutex
	pending   map[uint64]chan common.VolunteerReply
	idleTimer *time.Timer
}

func (vc *volunteerConn) signal(ctx context.Context, id uint64, form url.Values) (int, []byte, error) {
	b, err := json.Marshal(common.VolunteerRequest{ID: id, Form: form})
	if err != nil {
		return 0, nil, err
	}
	reply := make(chan common.VolunteerReply, 1)
	vc.mu.Lock()
	if vc.isClosed() {
		vc.mu.Unlock()
		return 0, nil, errVolunteerSocketUnsent
	}
	vc.pending[id] = reply
	vc.idleTimer.Stop()
	vc.mu.Unlock()
	defer vc.done(id)

	writeCtx, cancel := context.WithTimeout(context.Background(), volunteerSocketWriteTimeout)
	err = vc.c.Write(writeCtx, websocket.MessageText, b)
	cancel()
	if err != nil {
		// Not POSTed instead: Freddie may have had the request, and an answer it delivered twice
		// would get the second a 404, ending a good attempt.
		vc.close()
		return 0, nil, err
	}
	select {
	case r := <-reply:
		return r.Status, []byte(r.Body), nil
	case <-vc.closed:
		return 0, nil, errVolunteerSocketClosed
	case <-ctx.Done():
		return 0, nil, ctx.Err()
	}
}

// done forgets the request with ID id, and starts the idle timer if it was the last.
func (vc *volunteerConn) done(id uint64) {
	vc.mu.Lock()
	defer vc.mu.Unlock()
	delete(vc.pending, id)
	if len(vc.pending) == 0 {
		vc.idleTimer.Reset(volunteerSocketIdle)
	}
}

// closeIfIdle closes the socket if no request is waiting on it. It marks the socket closed under
// mu, so a request that comes in meanwhile sees it closed and goes as a POST.
func (vc *volunteerConn) closeIfIdle() {
	vc.mu.Lock()
	idle := len(vc.pending) == 0
	if idle {
		vc.closeOnce.Do(func() { close(vc.closed) })
	}
	vc.mu.Unlock()
	if idle {
		vc.c.CloseNow()
	}
}

// read hands each reply to the request waiting for it, until the socket closes.
func (vc *volunteerConn) read() {
	defer vc.close()
	for {
		typ, b, err := vc.c.Read(context.Background())
		if err != nil {
			return
		}
		var r common.VolunteerReply
		if typ != websocket.MessageText || json.Unmarshal(b, &r) != nil {
			return
		}
		vc.mu.Lock()
		reply := vc.pending[r.ID]
		vc.mu.Unlock()
		if reply != nil {
			select {
			case reply <- r:
			default:
			}
		}
	}
}

func (vc *volunteerConn) close() {
	vc.closeOnce.Do(func() { close(vc.closed) })
	vc.c.CloseNow()
}

func (vc *volunteerConn) isClosed() bool {
	select {
	case <-vc.closed:
		return true
	default:
		return false
	}
}

// postSignal POSTs form to Freddie's signaling endpoint. A body that can't be read comes back as
// an error along with the status.
func postSignal(ctx context.Context, options *WebRTCOptions, form url.Values) (int, []byte, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		"POST",
		options.DiscoverySrv+options.Endpoint,
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Add(common.VersionHeader, common.Version)

	res, err := options.HTTPClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return res.StatusCode, nil, err
	}
	return res.StatusCode, body, nil
}
