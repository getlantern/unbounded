package freddie

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/getlantern/broflake/common"
)

const (
	// volunteerMaxInFlight bounds the requests one socket may have waiting at once. A volunteer
	// has one per worker slot, and a few more while one rolls over to the next.
	volunteerMaxInFlight = 32
	// volunteerReadLimit bounds one request. An answer SDP with its candidates is a few KB.
	volunteerReadLimit = 64 << 10
	// volunteerPingInterval keeps NATs and load balancers from closing an idle socket, and finds a
	// dead one. volunteerPingTimeout bounds each ping.
	volunteerPingInterval = 30 * time.Second
	volunteerPingTimeout  = 10 * time.Second
)

// volunteerMessageTypes are the signaling messages a volunteer sends: a genesis, and its answer to
// the offer that genesis got.
var volunteerMessageTypes = map[string]bool{
	strconv.Itoa(int(common.SignalMsgGenesis)): true,
	strconv.Itoa(int(common.SignalMsgAnswer)):  true,
}

// GET /v1/volunteer upgrades to the volunteer socket (see common.VolunteerSocketEndpoint).
// Each request runs through postSignal, as its POST would, on its own goroutine, so a genesis
// waiting for an offer doesn't hold up the others.
func (f *Freddie) handleVolunteerSocket(w http.ResponseWriter, r *http.Request) {
	version := r.Header.Get(common.VersionHeader)
	if version == "" {
		version = r.URL.Query().Get(common.VolunteerSocketVersionParam)
	}
	if !common.IsValidVersion(version) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("418\n"))
		return
	}
	// Outcomes and preflights label the volunteer's version from the header, as for its POSTs.
	r.Header.Set(common.VersionHeader, version)
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// This skips the Origin check, not any TLS verification: the browser widget connects from
		// whatever page embeds it, as it POSTs to /v1/signal from any origin (enableCors), and the
		// socket carries no cookies or credentials an origin check would protect.
		InsecureSkipVerify: true,
	})
	if err != nil {
		return
	}
	defer c.CloseNow()
	c.SetReadLimit(volunteerReadLimit)

	// Deferred before cancel, so cancel runs first on every return and frees the requests still
	// waiting: a hijacked connection's r.Context() isn't cancelled when the volunteer goes away.
	var wg sync.WaitGroup
	defer wg.Wait()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	f.volunteerSockets.Add(ctx, 1)
	defer f.volunteerSockets.Add(context.Background(), -1)

	var writeMu sync.Mutex
	write := func(reply common.VolunteerReply) {
		b, err := json.Marshal(reply)
		if err != nil {
			return
		}
		writeMu.Lock()
		defer writeMu.Unlock()
		if err := c.Write(ctx, websocket.MessageText, b); err != nil {
			cancel()
		}
	}

	go func() {
		t := time.NewTicker(volunteerPingInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pingCtx, done := context.WithTimeout(ctx, volunteerPingTimeout)
				err := c.Ping(pingCtx)
				done()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	inFlight := make(chan struct{}, volunteerMaxInFlight)
	// cancels ends each request in flight by its ID, for a volunteer that gives up on one.
	var cancelsMu sync.Mutex
	cancels := make(map[uint64]context.CancelFunc)
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		var req common.VolunteerRequest
		if typ != websocket.MessageText || json.Unmarshal(b, &req) != nil {
			c.Close(websocket.StatusUnsupportedData, "expected a JSON volunteer request")
			return
		}
		if req.Cancel {
			cancelsMu.Lock()
			if cancelReq, ok := cancels[req.ID]; ok {
				cancelReq()
			}
			cancelsMu.Unlock()
			continue
		}
		if !volunteerMessageTypes[req.Form.Get("type")] {
			write(common.VolunteerReply{ID: req.ID, Status: http.StatusBadRequest})
			continue
		}
		select {
		case inFlight <- struct{}{}:
		default:
			write(common.VolunteerReply{ID: req.ID, Status: http.StatusTooManyRequests})
			continue
		}
		reqCtx, cancelReq := context.WithCancel(ctx)
		cancelsMu.Lock()
		cancels[req.ID] = cancelReq
		cancelsMu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-inFlight }()
			defer func() {
				cancelsMu.Lock()
				delete(cancels, req.ID)
				cancelsMu.Unlock()
				cancelReq()
			}()
			// net/http recovers a handler's panics, but not this goroutine's.
			defer func() {
				if p := recover(); p != nil {
					slog.Error("Panic handling a volunteer socket request", "panic", p, "stack", string(debug.Stack()))
					write(common.VolunteerReply{ID: req.ID, Status: http.StatusInternalServerError})
				}
			}()
			write(f.volunteerSignal(reqCtx, r, req))
		}()
	}
}

// volunteerSignal handles one request from a volunteer socket opened with upgrade, whose headers
// stand in for the POST's.
func (f *Freddie) volunteerSignal(ctx context.Context, upgrade *http.Request, req common.VolunteerRequest) common.VolunteerReply {
	ctx, span := f.tracer.Start(ctx, "handleVolunteerSignal")
	defer span.End()
	f.totalRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("method", "WS")))
	if f.RoomTicket == nil {
		f.roomsDisabled.Add(ctx, 1)
	}

	// A parsed form never holds a field with no values, and the code that reads one counts on
	// that, but JSON can say [] or null.
	form := url.Values{}
	for k, v := range req.Form {
		if len(v) > 0 {
			form[k] = v
		}
	}
	r := upgrade.Clone(ctx)
	r.Form, r.PostForm = form, form
	reply := f.postSignal(ctx, span, r, true)
	body := reply.body
	if reply.await != nil {
		body = reply.await(ctx.Done())
	}
	return common.VolunteerReply{ID: req.ID, Status: reply.status, Body: body}
}
