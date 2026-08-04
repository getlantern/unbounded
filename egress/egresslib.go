package egress

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/getlantern/broflake/common"
	"github.com/getlantern/telemetry"
)

// TODO: rate limiters and fancy settings and such:
// https://github.com/nhooyr/websocket/blob/master/examples/echo/server.go

const (
	websocketKeepalive = 15 * time.Second
)

// Multi-writer values used for logging and otel metrics
// nClients is the number of open WebSocket connections
var nClients uint64

// nQUICStreams is the number of open QUIC streams (not to be confused with QUIC connections)
var nQUICStreams uint64

// nQUICConnections is the number of open QUIC connections
var nQUICConnections uint64

var nClientsCounter metric.Int64ObservableUpDownCounter
var nQUICStreamsCounter metric.Int64ObservableUpDownCounter
var nQUICConnectionsCounter metric.Int64ObservableUpDownCounter
var nIngressBytesCounter metric.Int64ObservableUpDownCounter

// refusedCounter tallies connections turned away before a session span exists.
// A monotonic Counter, not an UpDownCounter: it is a cumulative tally meant to be
// rate()'d, unlike the concurrency gauges above.
var refusedCounter metric.Int64ObservableCounter

// tracer emits one span per WebSocket session. Sessions are the unit an
// operator actually asks about ("did this consumer get served?"), and a span
// per session carries the consumer session ID without the unbounded-cardinality
// problem that the same ID would cause as a metric label.
var tracer = otel.Tracer("github.com/getlantern/broflake/egress")

// Span and attribute names for the per-session spans.
const (
	spanWebSocketSession = "egress.websocket_session"

	attrConsumerSessionID = "broflake.consumer_session_id"
	attrDonorCountry      = "broflake.donor_country"
	attrConsumerCountry   = "broflake.consumer_country"
	attrIngressBytes      = "broflake.session_ingress_bytes"
	attrQUICStreams       = "broflake.session_quic_streams"
	attrTeardownReason    = "broflake.teardown_reason"
	attrProtocolVersion   = "broflake.protocol_version"
)

type proxyListener struct {
	net.Listener
	*connectionManager
	connections  chan net.Conn
	addr         net.Addr
	closeMetrics func(ctx context.Context) error
}

func (l proxyListener) Accept() (net.Conn, error) {
	conn := <-l.connections
	return conn, nil
}

func (l proxyListener) Addr() net.Addr {
	return l.addr
}

func (l proxyListener) Close() error {
	err := l.Listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	l.closeMetrics(ctx)
	return err
}

func (l proxyListener) handleWebsocket(w http.ResponseWriter, r *http.Request) {
	// Our subprotocols should be a slice containing a single comma-separated string. But weird browsers
	// could theoretically send multiple Sec-Websocket-Protocol headers, one for each subprotocol, which
	// would result in a slice containing multiple strings. We handle both cases:
	rawSubprotocols := r.Header[common.SubprotocolsHeader]
	joined := strings.Join(rawSubprotocols, ",")

	subprotocols := []string{}
	for _, sp := range strings.Split(joined, ",") {
		trimmed := strings.TrimSpace(sp)
		if trimmed != "" {
			subprotocols = append(subprotocols, trimmed)
		}
	}

	consumerSessionID, version, consumerCountry, ok := common.ParseSubprotocolsRequestWithCountry(subprotocols)
	if !ok {
		// ParseSubprotocolsRequestWithCountry returns !ok for an absent header,
		// a wrong element count, or a magic-cookie mismatch. Reporting all three
		// as "missing" would point an investigation at the wrong caller, so
		// split on whether the client sent anything at all.
		//
		// The element count is logged; the values are not. They are
		// client-controlled and unbounded in size, and one of them is a session
		// identifier.
		// Test the RAW header, not the filtered list: "Sec-WebSocket-Protocol: ,"
		// filters down to zero values, so keying off the filtered slice reported a
		// client that clearly sent something as though it had sent nothing.
		reason, msg := refusedMissingSubprotocols, "Refused WebSocket connection, missing subprotocols"
		if len(rawSubprotocols) > 0 {
			reason, msg = refusedMalformedSubprotocols, "Refused WebSocket connection, malformed subprotocols"
		}
		recordRefusal(reason)
		slog.Debug(msg, append(peerAttrs(r), "subprotocol_count", len(subprotocols))...)
		return
	}

	versionHeader := &http.Header{}
	versionHeader.Add(common.VersionHeader, version)

	if !common.IsValidProtocolVersion(versionHeader) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte("418\n"))
		recordRefusal(refusedBadProtocolVersion)
		slog.Debug("Refused WebSocket connection, bad protocol version",
			append(peerAttrs(r), "version", truncateForLog(version))...)
		return
	}

	// TODO: InsecureSkipVerify=true just disables origin checking, we need to instead add origin
	// patterns as strings using AcceptOptions.OriginPattern
	// TODO: disabling compression is a workaround for a WebKit bug:
	// https://github.com/getlantern/broflake/issues/45

	if consumerSessionID == "" {
		recordRefusal(refusedMissingCSID)
		slog.Debug("Refused WebSocket connection, missing consumer session ID", peerAttrs(r)...)
		return
	}

	// One span per WebSocket session. Started before websocket.Accept so that a
	// failed accept is still visible rather than vanishing, and parented to the
	// otelhttp request span. We discard the returned context deliberately: the
	// QUIC stream goroutine below runs on its own lifecycle (wsContext), and
	// threading a request-scoped context into it would tie stream teardown to
	// this handler's span rather than to the migration window.
	//
	// The donor country is not known until the peer address resolves after
	// Accept, so it is attached further down.
	_, span := tracer.Start(r.Context(), spanWebSocketSession, oteltrace.WithAttributes(
		attribute.String(attrConsumerSessionID, csidPrefix(consumerSessionID)),
		attribute.String(attrProtocolVersion, version),
	))
	if consumerCountry != "" {
		span.SetAttributes(attribute.String(attrConsumerCountry, consumerCountry))
	}

	// Per-session counters. These are the numbers that answer "did this session
	// actually carry traffic?", which the fleet-wide counters cannot.
	var sessionBytes int64
	var sessionStreams int64
	var keepaliveFailed atomic.Bool
	teardownReason := "websocket_closed"
	defer func() {
		// A wedged peer is distinguishable from a disconnected one only by an
		// unanswered keepalive, so let that outrank the generic close reason.
		if keepaliveFailed.Load() {
			teardownReason = "keepalive_timeout"
		}
		span.SetAttributes(
			attribute.Int64(attrIngressBytes, atomic.LoadInt64(&sessionBytes)),
			attribute.Int64(attrQUICStreams, atomic.LoadInt64(&sessionStreams)),
			attribute.String(attrTeardownReason, teardownReason),
		)
		// A session that moved no bytes is the failure mode worth finding, so
		// mark it on the span rather than leaving every session status Unset.
		if atomic.LoadInt64(&sessionBytes) == 0 {
			span.SetStatus(otelcodes.Error, "session carried no ingress bytes")
		}
		span.End()
	}()

	c, err := websocket.Accept(
		w,
		r,
		&websocket.AcceptOptions{
			InsecureSkipVerify: true,
			CompressionMode:    websocket.CompressionDisabled,
			Subprotocols:       common.NewSubprotocolsResponse(),
		},
	)
	if err != nil {
		teardownReason = "websocket_accept_failed"
		span.RecordError(err)
		slog.Debug("Error accepting WebSocket connection", "error", err)
		return
	}

	tcpAddr, err := net.ResolveTCPAddr("tcp", r.RemoteAddr)
	if err != nil {
		// c is already accepted at this point but wspconn — and its deferred
		// Close — is not built until below, so this path leaked the connection
		// and its goroutines. Pre-existing on main; closing it here since this
		// branch is being touched anyway.
		c.CloseNow()
		teardownReason = "peer_addr_unresolvable"
		span.RecordError(err)
		slog.Debug("Error resolving TCPAddr", "error", err)
		return
	}

	// Resolved once per session, never per packet: this is a database lookup and
	// the read path is hot.
	donorCC := donorCountry(tcpAddr)
	stats := statsFor(donorCC)
	span.SetAttributes(attribute.String(attrDonorCountry, donorCC))

	atomic.AddInt64(&stats.clients, 1)
	defer atomic.AddInt64(&stats.clients, -1)

	wspconn := errorlessWebSocketPacketConn{
		w:               c,
		addr:            common.DebugAddr(fmt.Sprintf("WebSocket connection %v", uuid.NewString())),
		keepalive:       websocketKeepalive,
		tcpAddr:         tcpAddr,
		readError:       make(chan error),
		stats:           stats,
		sessionBytes:    &sessionBytes,
		keepaliveFailed: &keepaliveFailed,
	}

	defer wspconn.Close()
	slog.Debug("Accepted a new WebSocket connection!", "csid", csidPrefix(consumerSessionID), "donor_country", donorCC, "total", atomic.AddUint64(&nClients, 1))

	conn, err := l.connectionManager.createOrMigrate(consumerSessionID, &wspconn)
	if err != nil {
		teardownReason = "create_or_migrate_failed"
		span.RecordError(err)
		slog.Debug("createOrMigrate error, closing!", "error", err)
		return
	}

	// Here we enter the steady state for the WebSocket tunnel and continue until there's some reason
	// to tear the tunnel down. An explainer about teardown: teardown begins when we intercept a read
	// error on the errorlessWebSocketPacketConn, which indicates that the underlying websocket.Conn
	// is no longer connected. (See commentary around errorlessWebSocketPacketConn for more context
	// around error interception). When we intercept a read error on the errorlessWebSocketPacketConn,
	// we wait for a bounded duration of time (the "migration window"), and then we delete the QUIC
	// connection state from the connection manager if it has not been migrated within that window.
	// The deletion operation will cause AcceptStream (below) to return an error, which returns from
	// and cleans up the stream handling goroutine. If the QUIC connection DID migrate within the
	// migration window, we keep its state intact, and we forcibly kill the stream handling goroutine
	// for *this WebSocket* by cancelling wsContext. In both cases, we then return from this function,
	// which cleans up the WebSocket resource. In operation, you will observe WebSockets "dangle" for
	// a duration of time equal to migrationWindow, and the total number of WebSocket connections
	// logged by the server will eventually converge to the correct value when the server has quiesced.
	wsContext, wsCancel := context.WithCancel(context.Background())
	QUICLayerError := make(chan struct{}, 1)

	go func() {
		for {
			stream, err := conn.AcceptStream(wsContext)

			if err != nil {
				slog.Debug("QUIC AcceptStream error, terminating handler", "addr", wspconn.addr, "error", err)
				QUICLayerError <- struct{}{}
				close(QUICLayerError)
				return
			}
			atomic.AddInt64(&sessionStreams, 1)
			slog.Debug("Accepted a new QUIC stream!", "total", atomic.AddUint64(&nQUICStreams, 1))

			l.connections <- common.QUICStreamNetConn{
				Stream: stream,
				OnClose: func() {
					defer slog.Debug("Closed a QUIC stream!", "total", atomic.AddUint64(&nQUICStreams, ^uint64(0)))
				},
				AddrLocal:  l.addr,
				AddrRemote: tcpAddr,
			}
		}
	}()

	select {
	case <-wspconn.readError:
		// Normal *outside-in* tunnel collapse: on the first read error intercepted at the WebSocket
		// layer, we initiate the migration procedure, delete the inner QUIC layer connection state if
		// necessary, then return from handleWebsocket.
		slog.Debug("read error, waiting for migration...", "addr", wspconn.addr, "migration_window_s", l.connectionManager.migrationWindow.Seconds())

		t1 := time.Now()
		<-time.After(l.connectionManager.migrationWindow)
		l.connectionManager.deleteIfNotMigratedSince(consumerSessionID, t1)
		wsCancel()
	case <-QUICLayerError:
		// Unexpected *inside-out* tunnel collapse: we should only enter this path if there's a bug. If
		// we're here, it means there was an AcceptStream error on a QUIC connection that we didn't
		// initiate as part of our orderly outside-in tunnel collapse. This can happen, for example,
		// if the QUIC connection times out due to inactivity. To resynchronize, we delete the QUIC
		// connection state and return from handleWebsocket, closing the tunnel completely.
		l.connectionManager.deleteIfNotMigratedSince(consumerSessionID, time.Now().Add(24*time.Hour))
	}
}

func NewListener(ctx context.Context, ll net.Listener, tlsConfig *tls.Config) (net.Listener, error) {
	closeFuncMetrics := telemetry.EnableOTELMetrics(ctx)

	// Tracing powers the per-session spans in handleWebsocket. Enabled alongside
	// metrics rather than instead of them: the counters answer "is the fleet
	// carrying traffic", the spans answer "did this particular consumer session
	// get served", and neither substitutes for the other.
	closeFuncTracing := telemetry.EnableOTELTracing(ctx)

	// Shut both down together on listener close. Dropping the tracing shutdown
	// would leak the provider and discard whatever spans were still buffered,
	// which on a low-traffic egress could be most of them.
	closeFuncMetric := func(ctx context.Context) error {
		errMetrics := closeFuncMetrics(ctx)
		errTracing := closeFuncTracing(ctx)
		return errors.Join(errMetrics, errTracing)
	}

	// Geolocation is optional; without GEODB every series is labelled "unknown".
	initDonorGeo()

	m := otel.Meter("github.com/getlantern/broflake/egress")
	var err error
	nClientsCounter, err = m.Int64ObservableUpDownCounter("concurrent-websockets")
	if err != nil {
		closeFuncMetric(ctx)
		return nil, err
	}

	nQUICConnectionsCounter, err = m.Int64ObservableUpDownCounter("concurrent-quic-connections")
	if err != nil {
		closeFuncMetric(ctx)
		return nil, err
	}

	nQUICStreamsCounter, err = m.Int64ObservableUpDownCounter("concurrent-quic-streams")
	if err != nil {
		closeFuncMetric(ctx)
		return nil, err
	}

	nIngressBytesCounter, err = m.Int64ObservableUpDownCounter("ingress-bytes")
	if err != nil {
		closeFuncMetric(ctx)
		return nil, err
	}

	refusedCounter, err = m.Int64ObservableCounter("refused-websockets")
	if err != nil {
		closeFuncMetric(ctx)
		return nil, err
	}

	_, err = m.RegisterCallback(
		func(ctx context.Context, o metric.Observer) error {
			q := atomic.LoadUint64(&nQUICConnections)
			o.ObserveInt64(nQUICConnectionsCounter, int64(q))

			s := atomic.LoadUint64(&nQUICStreams)
			o.ObserveInt64(nQUICStreamsCounter, int64(s))

			// concurrent-websockets and ingress-bytes are now reported per donor
			// country rather than as a single unlabelled series. Totals are
			// preserved: summing across the country dimension gives the same
			// number the unlabelled series carried, so queries that don't group
			// by country are unaffected. Anything that reduced with max/latest
			// instead of sum needs a spaceAggregation of sum to stay correct.
			//
			// CAUTION when summing: these datapoints also carry a `via` resource
			// attribute identifying the telemetry collector that forwarded them
			// (ops-0/1/2), and the same datapoint arrives once per collector. The
			// egress is a single instance — instance.id has exactly one value,
			// unbounded-us-linode-nj.iantem.io — so summing across `via` triples
			// the real figure. Sum across donor_country, but filter or average
			// across `via`.
			//
			// Note these counts come from per-session counters incremented and
			// decremented exactly once around the handler, whereas the legacy
			// global nClients decrements in the conn's Close(). nClients is now
			// only used for the log lines.
			eachRefusal(func(reason refusalReason, count int64) {
				o.ObserveInt64(refusedCounter, count,
					metric.WithAttributes(attribute.String("reason", string(reason))))
			})

			eachCountryStats(func(cc string, clients, ingressBytes int64) {
				attrs := metric.WithAttributes(attribute.String(attrDonorCountry, cc))
				o.ObserveInt64(nClientsCounter, clients, attrs)
				o.ObserveInt64(nIngressBytesCounter, ingressBytes, attrs)
			})
			return nil
		},
		nClientsCounter,
		nQUICConnectionsCounter,
		nQUICStreamsCounter,
		nIngressBytesCounter,
		refusedCounter,
	)
	if err != nil {
		closeFuncMetric(ctx)
		return nil, err
	}

	cm := &connectionManager{
		connections:     make(map[string]*connectionRecord),
		tlsConfig:       tlsConfig,
		migrationWindow: 30 * time.Second,
		probeTimeout:    35 * time.Second,
	}

	// We use this wrapped listener to enable our local HTTP proxy to listen for WebSocket connections
	l := proxyListener{
		Listener:          ll,
		connectionManager: cm,
		connections:       make(chan net.Conn, 2048),
		addr:              ll.Addr(),
		closeMetrics:      closeFuncMetric,
	}

	// Use a fresh ServeMux per listener rather than http.DefaultServeMux.
	// Registering on the default mux here previously made this function
	// unsafe to call twice in the same process — the second call would
	// panic on duplicate `/ws` registration, which broke tests, graceful
	// restarts, and any host that embeds multiple egress listeners.
	mux := http.NewServeMux()
	// Wrap the handler for span propagation only — explicitly attach a noop
	// MeterProvider so otelhttp does NOT emit http.server.* histograms here.
	// The default attribute set on those histograms includes net.sock.peer.addr,
	// net.sock.peer.port and http.user_agent, which together create a fresh
	// time series for every WebSocket connection (~thousands/day on a single
	// egress) and blow up SigNoz cardinality. The four ObservableUpDownCounters
	// above already cover the only useful signals (concurrent ws/quic/streams,
	// ingress bytes); per-request HTTP metrics on a single upgrade endpoint
	// add no information.
	mux.Handle("/ws", otelhttp.NewHandler(http.HandlerFunc(l.handleWebsocket), "/ws",
		otelhttp.WithMeterProvider(metricnoop.NewMeterProvider())))

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	slog.Debug("Egress server listening for WebSocket connections", "addr", ll.Addr())
	go func() {
		err := srv.Serve(ll)
		// srv.Serve always returns a non-nil error, but a clean shutdown (the
		// listener was closed or http.Server.Shutdown was called) returns one
		// of a known set that callers — including our tests and any graceful
		// restart path — should treat as non-fatal. Only panic when the error
		// is genuinely unexpected.
		if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
			slog.Debug("Egress server stopped listening cleanly", "error", err)
			return
		}
		panic(fmt.Sprintf("egress server stopped listening unexpectedly: %v", err))
	}()

	return l, nil
}
