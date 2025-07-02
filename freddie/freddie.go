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

const (
	consumerTTL = 20
	msgTTL      = 5
	bufferSz    = 1000
)

var (
	consumerTable = userTable{Data: make(map[string]chan string)}
	signalTable   = userTable{Data: make(map[string]chan string)}
)

type userTable struct {
	Data map[string]chan string
	sync.RWMutex
}

func (t *userTable) Add(userID string) chan string {
	t.Lock()
	defer t.Unlock()
	t.Data[userID] = make(chan string, bufferSz)
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
		fmt.Fprintf(w, "freddie (%v)\n", common.Version)
		fmt.Fprintf(w, "current GET requests: %d\n", f.currentGets.Load())
		fmt.Fprintf(w, "current POST requests: %d\n", f.currentPosts.Load())
	})
	mux.HandleFunc("/v1/signal", f.handleSignal)

	return &f, nil
}

func (f *Freddie) ListenAndServe() error {
	common.Debugf("Freddie (%v) listening on %v", common.Version, f.srv.Addr)
	return f.srv.ListenAndServe()
}

func (f *Freddie) ListenAndServeTLS(certFile, keyFile string) error {
	f.srv.TLSConfig = f.TLSConfig

	common.Debugf("Freddie (%v/tls) listening on %v", common.Version, f.srv.Addr)
	return f.srv.ListenAndServeTLS(certFile, keyFile)
}

func (f *Freddie) Shutdown() error {
	return f.srv.Shutdown(f.ctx)
}

func (f *Freddie) handleSignal(w http.ResponseWriter, r *http.Request) {
	ctx, span := f.tracer.Start(r.Context(), "handleSignal")
	defer span.End()

	enableCors(w)

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
	defer func() { close(consumerChan) }()
	defer consumerTable.Delete(consumerID)

	// TODO: Matchmaking would happen here. (Just be selective about which consumers you broadcast
	// to, and you've implemented matchmaking!) If consumerTable was an indexed datastore, we could
	// select slices of consumers in O(1) based on some deterministic function
	w.WriteHeader(http.StatusOK)
	timeoutChan := time.After(consumerTTL * time.Second)

	for {
		select {
		case msg := <-consumerChan:
			fmt.Fprintf(w, "%v\n", msg)
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
	defer func() { close(reqChan) }()
	defer signalTable.Delete(reqID)

	if err := r.ParseForm(); err != nil {
		span.SetStatus(codes.Error, "failed to parse form")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("400\n"))
		return
	}
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

	// XXX: If the sender has just sent a SignalMsgICE, there are no more steps in the signaling
	// handshake, so we'll close the request immediately. Being aware of message contents here is
	// very un-Freddie-like! We previously implemented this short circuit behavior on the client side,
	// but it required a Flush() here to push the status header to the client. The Flush() confuses
	// the browser and breaks Golang context contracts in wasm build targets, so we live with this hack.
	if common.SignalMsgType(msgType) == common.SignalMsgICE {
		w.Write(nil)
		return
	}

	select {
	case res := <-reqChan:
		fmt.Fprintf(w, "%v\n", res)
	case <-time.After(msgTTL * time.Second):
		span.AddEvent("timeout waiting for response")
		w.Write(nil)
	}
}

// TODO: delete me and replace with a real CORS strategy!
func enableCors(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
}

// Validate the Broflake protocol version header. If the header isn't present, we consider you
// invalid. Protocol version is currently the major version of Broflake's reference implementation
func isValidProtocolVersion(r *http.Request) bool {
	return semver.Major(r.Header.Get(common.VersionHeader)) == semver.Major(common.Version)
}
