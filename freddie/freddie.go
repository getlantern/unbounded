package freddie

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/mod/semver"

	"github.com/getlantern/broflake/common"
)

// About these TTL values: Freddie models each segment of the signaling handshake as a request and
// a response. When Bob POSTs a signaling message to Freddie, Freddie delivers it to Alice, and then
// Freddie waits for Alice to POST a response. Here, "TTL" is the length of time Freddie will wait
// for Alice to send her response. In the past, Freddie was aggressively agnostic about the contents
// of each signaling message, but these days we find it helpful to break that agonisticism a bit, and
// for Freddie to tweak the TTL on a per-message basis to accommodate steps in the signaling
// handshake which require more or less time. For example, ICE gathering can take quite a long time,
// particularly under censored conditions, and so segments of the signaling handshake which await
// remote ICE candidates should accordingly wait a bit longer for a response.

// consumerTTL = how long to hold open HTTP requests for the genesis message stream?
// remoteICEGatheringTTL = TTL for segments awaiting remote ICE gathering
// defaultMsgTTL = TTL for all other segments

const (
	consumerTTL           = 20
	defaultMsgTTL         = 5
	remoteICEGatheringTTL = 15
)

var (
	consumerTable = userTable{Data: make(map[string]chan string)}
	signalTable   = userTable{Data: make(map[string]chan string)}
)

// A userTable keeps state for requests and responses. A request handler calls Add() to create a
// channel for a responder to respond over. Since we cannot guarantee a that a responder will arrive,
// request handlers should eventually call Delete() to clean up that channel state. Send() implements
// exactly-once semantics by deleting the channel from the userTable after sending over it. Thus,
// we create a race between the request handler and response handler to idempotently delete the
// response channel state: if a responder arrives and sends a response over the channel, they will
// delete it, and if the responder fails to arrive, the request handler will delete it upon timeout.
type userTable struct {
	Data map[string]chan string
	sync.RWMutex
}

func (t *userTable) Add(userID string) chan string {
	t.Lock()
	defer t.Unlock()
	t.Data[userID] = make(chan string, 1)
	return t.Data[userID]
}

func (t *userTable) Delete(userID string) {
	t.Lock()
	defer t.Unlock()
	delete(t.Data, userID)
}

func (t *userTable) Send(userID string, msg string) bool {
	t.Lock()
	defer t.Unlock()
	userChan, ok := t.Data[userID]
	if ok {
		userChan <- msg
		delete(t.Data, userID)
	}
	return ok
}

func (t *userTable) SendAll(msg string) {
	t.Lock()
	defer t.Unlock()
	for _, userChan := range t.Data {
		userChan <- msg
	}
}

func (t *userTable) Size() int {
	t.RLock()
	defer t.RUnlock()
	return len(t.Data)
}

type Freddie struct {
	TLSConfig *tls.Config

	ctx context.Context
	srv *http.Server

	currentGets  atomic.Int64
	currentPosts atomic.Int64

	tracer            trace.Tracer
	meter             metric.Meter
	nConcurrentReqs   metric.Int64ObservableUpDownCounter
	totalRequests     metric.Int64Counter
	consumerTableSize metric.Int64ObservableUpDownCounter
	signalTableSize   metric.Int64ObservableUpDownCounter
}

func New(ctx context.Context, listenAddr string) (*Freddie, error) {
	mux := http.NewServeMux()

	f := Freddie{
		ctx: ctx,
		srv: &http.Server{
			ReadTimeout:  30 * time.Second,
			WriteTimeout: 30 * time.Second,
			Addr:         listenAddr,
			Handler:      mux,
		},
		currentGets:  atomic.Int64{},
		currentPosts: atomic.Int64{},
		tracer:       otel.Tracer("github.com/getlantern/broflake/freddie"),
		meter:        otel.Meter("github.com/getlantern/broflake/freddie"),
	}

	var err error

	f.nConcurrentReqs, err = f.meter.Int64ObservableUpDownCounter("freddie.requests.concurrent",
		metric.WithDescription("concurrent requests"),
		metric.WithUnit("request"),
		metric.WithInt64Callback(func(ctx context.Context, m metric.Int64Observer) error {
			attrs := metric.WithAttributes(attribute.String("method", "GET"))
			m.Observe(f.currentGets.Load(), attrs)

			attrs = metric.WithAttributes(attribute.String("method", "POST"))
			m.Observe(f.currentPosts.Load(), attrs)
			return nil
		}))
	if err != nil {
		return nil, err
	}

	f.totalRequests, err = f.meter.Int64Counter("freddie.requests",
		metric.WithDescription("total requests"),
		metric.WithUnit("request"))
	if err != nil {
		return nil, err
	}

	f.consumerTableSize, err = f.meter.Int64ObservableUpDownCounter("freddie.consumertable.size",
		metric.WithDescription("total number of users in the consumers table"),
		metric.WithUnit("user"),
		metric.WithInt64Callback(func(ctx context.Context, m metric.Int64Observer) error {
			m.Observe(int64(consumerTable.Size()))
			return nil
		}))
	if err != nil {
		return nil, err
	}

	f.signalTableSize, err = f.meter.Int64ObservableUpDownCounter("freddie.signaltable.size",
		metric.WithDescription("total number of users in the signal table"),
		metric.WithUnit("user"),
		metric.WithInt64Callback(func(ctx context.Context, m metric.Int64Observer) error {
			m.Observe(int64(signalTable.Size()))
			return nil
		}))
	if err != nil {
		return nil, err
	}

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf("freddie (%v)\n", common.Version)))
		w.Write([]byte(fmt.Sprintf("current GET requests: %d\n", f.currentGets.Load())))
		w.Write([]byte(fmt.Sprintf("current POST requests: %d\n", f.currentPosts.Load())))
	})
	mux.HandleFunc("/v1/signal", f.handleSignal)

	return &f, nil
}

func (f *Freddie) ListenAndServe() error {
	common.Debugf(
		"Freddie (%v) listening on %v (consumerTTL: %v remoteICEGatheringTTL: %v defaultMsgTTL: %v)",
		common.Version,
		f.srv.Addr,
		consumerTTL,
		remoteICEGatheringTTL,
		defaultMsgTTL,
	)
	return f.srv.ListenAndServe()
}

func (f *Freddie) ListenAndServeTLS(certFile, keyFile string) error {
	f.srv.TLSConfig = f.TLSConfig

	common.Debugf(
		"Freddie (%v/tls) listening on %v (consumerTTL: %v remoteICEGatheringTTL: %v defaultMsgTTL: %v)",
		common.Version,
		f.srv.Addr,
		consumerTTL,
		remoteICEGatheringTTL,
		defaultMsgTTL,
	)
	return f.srv.ListenAndServeTLS(certFile, keyFile)
}

func (f *Freddie) Shutdown() error {
	return f.srv.Shutdown(f.ctx)
}

func (f *Freddie) handleSignal(w http.ResponseWriter, r *http.Request) {
	ctx, span := f.tracer.Start(r.Context(), "handleSignal")
	defer span.End()

	enableCors(&w)

	// Handle preflight requests
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Credentials", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,HEAD,OPTIONS,POST,PUT")
		w.Header().Set(
			"Access-Control-Allow-Headers",
			"Access-Control-Allow-Headers, Origin, Accept, X-Requested-With, Content-Type, "+
				"Access-Control-Request-Method, Access-Control-Request-Headers, "+common.VersionHeader,
		)

		w.WriteHeader(http.StatusOK)
		return
	}

	if !isValidProtocolVersion(r) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("418\n"))
		return
	}

	f.totalRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("method", r.Method)))

	switch r.Method {
	case http.MethodGet:
		f.handleSignalGet(ctx, w, r)
	case http.MethodPost:
		f.handleSignalPost(ctx, w, r)
	}
}

// GET /v1/signal is the producer advertisement stream
func (f *Freddie) handleSignalGet(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ctx, span := f.tracer.Start(ctx, "handleSignalGet")
	defer span.End()

	f.currentGets.Add(1)
	defer f.currentGets.Add(-1)

	consumerID := uuid.NewString()
	span.SetAttributes(attribute.String("consumer.id", consumerID))

	consumerChan := consumerTable.Add(consumerID)
	defer consumerTable.Delete(consumerID)

	// TODO: Matchmaking would happen here. (Just be selective about which consumers you broadcast
	// to, and you've implemented matchmaking!) If consumerTable was an indexed datastore, we could
	// select slices of consumers in O(1) based on some deterministic function
	w.WriteHeader(http.StatusOK)
	timeoutChan := time.After(consumerTTL * time.Second)

	for {
		select {
		case msg := <-consumerChan:
			w.Write([]byte(fmt.Sprintf("%v\n", msg)))
			w.(http.Flusher).Flush()
		case <-timeoutChan:
			return
		}
	}
}

// POST /v1/signal is how all signaling messaging is performed
func (f *Freddie) handleSignalPost(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	ctx, span := f.tracer.Start(ctx, "handleSignalPost")
	defer span.End()

	f.currentPosts.Add(1)
	defer f.currentPosts.Add(-1)

	reqID := uuid.NewString()
	span.SetAttributes(attribute.String("request.id", reqID))

	reqChan := signalTable.Add(reqID)
	defer signalTable.Delete(reqID)

	r.ParseForm()
	sendTo := r.Form.Get("send-to")
	data := r.Form.Get("data")
	msgType, err := strconv.ParseInt(r.Form.Get("type"), 10, 32)
	if err != nil {
		span.SetStatus(codes.Error, "invalid message type")
		span.RecordError(fmt.Errorf("invalid message type: %w", err))
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("400\n"))
		return
	}

	span.SetAttributes(
		attribute.String("recipient.id", sendTo),
		attribute.String("msg_type", common.SignalMsgType(msgType).String()),
	)

	// Package the message
	msg, err := json.Marshal(
		common.SignalMsg{ReplyTo: reqID, Type: common.SignalMsgType(msgType), Payload: data},
	)
	if err != nil {
		// Malformed request
		span.SetStatus(codes.Error, "malformed request")
		span.RecordError(fmt.Errorf("malformed request: %w", err))
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("400\n"))
		return
	}

	if sendTo == "genesis" {
		// It's a genesis message, so let's broadcast it to all consumers
		consumerTable.SendAll(string(msg))
	} else {
		// It's a regular message, so let's signal it to its recipient (or return a 404 if the
		// recipient is no longer available)
		ok := signalTable.Send(sendTo, string(msg))
		if !ok {
			span.SetStatus(codes.Error, "recipient not found")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte("404\n"))
			return
		}
	}

	// Send 200 OK to indicate that signaling partner accepts the message, stream back their
	// response or a nil body if they failed to respond
	w.WriteHeader(http.StatusOK)
	span.SetStatus(codes.Ok, "")

	// XXX: below, Freddie becomes interested in the content of the signaling message so as to
	// implement optimizations, mostly related to synchronization and timing. This makes everything
	// less maintainable, so we dream of a world where Freddie can become agnostic again. That world
	// becomes real when we can move this logic to the client FSMs...
	var optimizedTTL time.Duration
	switch common.SignalMsgType(msgType) {
	case common.SignalMsgOffer:
		// Upon successfully delivering an offer, we wait for the remote peer to complete ICE gathering
		optimizedTTL = remoteICEGatheringTTL * time.Second
	case common.SignalMsgAnswer:
		// Upon successfully delivering an answer, we wait for the remote peer to complete ICE gathering
		optimizedTTL = remoteICEGatheringTTL * time.Second
	case common.SignalMsgICE:
		// There are no more steps in the signaling handshake, so just close the request immediately.
		// We previously implemented this short circuit behavior on the client side, but it required a
		// Flush() here to push the status header to the client. The Flush() confuses the browser and
		// breaks Golang context contracts in Wasm build targets, so we live with this for now.
		w.Write(nil)
		return
	default:
		optimizedTTL = defaultMsgTTL * time.Second
	}

	select {
	case res := <-reqChan:
		w.Write([]byte(fmt.Sprintf("%v\n", res)))
	case <-time.After(optimizedTTL):
		span.AddEvent("timeout waiting for response")
		w.Write(nil)
	}
}

// TODO: delete me and replace with a real CORS strategy!
func enableCors(w *http.ResponseWriter) {
	(*w).Header().Set("Access-Control-Allow-Origin", "*")
}

// Validate the Broflake protocol version header. If the header isn't present, we consider you
// invalid. Protocol version is currently the major version of Broflake's reference implementation
func isValidProtocolVersion(r *http.Request) bool {
	if semver.Major(r.Header.Get(common.VersionHeader)) != semver.Major(common.Version) {
		return false
	}

	return true
}
