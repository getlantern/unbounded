package egress

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/getlantern/broflake/common"
)

// startHelloConsumer is startTestConsumer, plus a consumer hello sent on
// every connection it accepts when hello is non-nil.
func startHelloConsumer(t *testing.T, hello *common.ConsumerHello) net.PacketConn {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket consumer: %v", err)
	}
	tr := &quic.Transport{Conn: pc}
	listener, err := tr.Listen(testServerTLS(), &common.QUICCfg)
	if err != nil {
		_ = pc.Close()
		t.Fatalf("Transport.Listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close(); _ = pc.Close() })

	go func() {
		for {
			conn, err := listener.Accept(context.Background())
			if err != nil {
				return
			}
			if hello != nil {
				go func() {
					s, err := conn.OpenUniStreamSync(context.Background())
					if err != nil {
						return
					}
					_ = common.WriteConsumerHello(s, *hello)
					_ = s.Close()
				}()
			}
			go func() {
				for {
					s, err := conn.AcceptStream(context.Background())
					if err != nil {
						return
					}
					go func() {
						buf := make([]byte, 4096)
						for {
							if _, err := s.Read(buf); err != nil {
								return
							}
						}
					}()
				}
			}()
		}
	}()
	return pc
}

func newHelloTestManager() *connectionManager {
	return &connectionManager{
		connections:     map[string]*connectionRecord{},
		tlsConfig:       testClientTLS(),
		migrationWindow: 5 * time.Second,
		probeTimeout:    5 * time.Second,
	}
}

func dialHelloTestConsumer(t *testing.T, cm *connectionManager, consumer net.PacketConn, csid string) *quic.Conn {
	t.Helper()
	pconn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	t.Cleanup(func() { _ = pconn.Close() })
	conn, _, err := cm.createOrMigrate(csid, dialedPconn{PacketConn: pconn, dst: consumer.LocalAddr()})
	if err != nil {
		t.Fatalf("createOrMigrate: %v", err)
	}
	t.Cleanup(func() { closeAllRecords(cm) })
	return conn
}

func waitForHello(slot *consumerHelloSlot, timeout time.Duration) common.ConsumerHello {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if h := slot.load(); h != (common.ConsumerHello{}) {
			return h
		}
		time.Sleep(10 * time.Millisecond)
	}
	return slot.load()
}

func TestConsumerHello_ReachesSessionSlot(t *testing.T) {
	want := common.ConsumerHello{Tag: "unbounded-out-trackA-r42", ClientVersion: "9.1.0", Platform: "android"}
	consumer := startHelloConsumer(t, &want)
	cm := newHelloTestManager()
	conn := dialHelloTestConsumer(t, cm, consumer, "csid-hello")

	slot := cm.consumerHelloFor(conn)
	if slot == nil {
		t.Fatal("no hello slot for a freshly dialed connection")
	}
	if got := waitForHello(slot, 5*time.Second); got != want {
		t.Fatalf("hello = %+v, want %+v", got, want)
	}
}

func TestConsumerHello_AbsentLeavesProxyingIntact(t *testing.T) {
	consumer := startHelloConsumer(t, nil)
	cm := newHelloTestManager()
	conn := dialHelloTestConsumer(t, cm, consumer, "csid-no-hello")

	s, err := openWritableStream(t, conn)
	if err != nil {
		t.Fatalf("bidirectional stream without a hello: %v", err)
	}
	_ = s.Close()
	if got := cm.consumerHelloFor(conn).load(); got != (common.ConsumerHello{}) {
		t.Fatalf("hello = %+v, want none", got)
	}
}

func TestConsumerHello_SlotDroppedWhenConnectionCloses(t *testing.T) {
	consumer := startHelloConsumer(t, nil)
	cm := newHelloTestManager()
	conn := dialHelloTestConsumer(t, cm, consumer, "csid-close")

	_ = conn.CloseWithError(0, "test")
	deadline := time.Now().Add(5 * time.Second)
	for cm.consumerHelloFor(conn) != nil {
		if time.Now().After(deadline) {
			t.Fatal("hello slot outlived its connection")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLogSessionEnd(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	logSessionEnd(sessionSummary{
		csid:            "0123456789abcdef",
		protocolVersion: "v2.3.17",
		consumerCountry: "IR",
		donorCountry:    "DE",
		ingressBytes:    2048,
		quicStreams:     3,
		teardown:        teardownWebSocketClosed,
		duration:        90 * time.Second,
		hello:           common.ConsumerHello{Tag: "unbounded-out-trackA-r42", Platform: "android"},
	})

	line := buf.String()
	for _, want := range []string{
		"level=INFO",
		`msg="WebSocket session ended"`,
		attrConsumerSessionID + "=01234567 ",
		attrIngressBytes + "=2048",
		attrSessionDuration + "=90",
		attrConsumerCountry + "=IR",
		attrConsumerTag + "=unbounded-out-trackA-r42",
		attrConsumerPlatform + "=android",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("log line missing %q:\n%s", want, line)
		}
	}
	if strings.Contains(line, attrConsumerVersion) {
		t.Errorf("empty hello field was logged:\n%s", line)
	}
}
