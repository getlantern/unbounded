package freddie

import (
	"context"
	"encoding/json"
	"net/http"
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
	if !common.IsValidProtocolVersion(&r.Header) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("418\n"))
		return
	}
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
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		typ, b, err := c.Read(ctx)
		if err != nil {
			cancel()
			return
		}
		var req common.VolunteerRequest
		if typ != websocket.MessageText || json.Unmarshal(b, &req) != nil {
			c.Close(websocket.StatusUnsupportedData, "expected a JSON volunteer request")
			return
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-inFlight }()
			write(f.volunteerSignal(ctx, r, req))
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

	r := upgrade.Clone(ctx)
	r.Form, r.PostForm = req.Form, req.Form
	reply := f.postSignal(ctx, span, r)
	body := reply.body
	if reply.await != nil {
		body = reply.await(ctx.Done())
	}
	return common.VolunteerReply{ID: req.ID, Status: reply.status, Body: body}
}
