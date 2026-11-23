package clientcore

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"
)

// pipeLayer is a ReliableStreamLayer whose streams are net.Pipe pairs; each
// dial hands the far end to egress.
type pipeLayer struct {
	egress func(net.Conn)
}

func (l pipeLayer) DialContext(context.Context) (net.Conn, error) {
	client, server := net.Pipe()
	go l.egress(server)
	return client, nil
}

// answerGreeting reads the client greeting and accepts no-auth.
func answerGreeting(t *testing.T, c net.Conn) {
	greeting := make([]byte, 3)
	if _, err := io.ReadFull(c, greeting); err != nil {
		return
	}
	c.Write([]byte{0x05, 0x00})
}

// readConnect consumes a CONNECT request for an IPv4 destination.
func readConnect(c net.Conn) error {
	_, err := io.ReadFull(c, make([]byte, 4+4+2))
	return err
}

func TestSOCKS5Dialer_ConnectsAndClearsDeadline(t *testing.T) {
	served := make(chan net.Conn, 1)
	dial := CreateSOCKS5Dialer(pipeLayer{egress: func(c net.Conn) {
		answerGreeting(t, c)
		if readConnect(c) != nil {
			return
		}
		// Reply byte by byte: the handshake must not depend on a single
		// Read returning a whole message.
		for _, b := range []byte{0x05, 0x00, 0x00, 0x01, 10, 0, 0, 1, 0x1f, 0x90} {
			c.Write([]byte{b})
		}
		served <- c
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	conn, err := dial(ctx, "tcp", "1.2.3.4:80")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	server := <-served

	// The handshake's deadline must not outlive it.
	time.Sleep(300 * time.Millisecond)
	go server.Write([]byte("hello"))
	buf := make([]byte, 5)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("conn must stay usable after the dial context's deadline: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("read %q, want %q", buf, "hello")
	}
}

func TestSOCKS5Dialer_StalledEgressIsBoundedByDeadline(t *testing.T) {
	for _, tt := range []struct {
		name  string
		stall func(t *testing.T, c net.Conn)
	}{
		{"no greeting reply", func(t *testing.T, c net.Conn) {
			io.ReadFull(c, make([]byte, 3))
		}},
		{"no CONNECT reply", func(t *testing.T, c net.Conn) {
			answerGreeting(t, c)
			readConnect(c)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			closed := make(chan struct{})
			dial := CreateSOCKS5Dialer(pipeLayer{egress: func(c net.Conn) {
				tt.stall(t, c)
				// The client closing its end shows up as EOF here.
				io.Copy(io.Discard, c)
				close(closed)
			}})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()

			start := time.Now()
			conn, err := dial(ctx, "tcp", "1.2.3.4:80")
			if err == nil || conn != nil {
				t.Fatalf("dial = %v, %v; want nil conn and an error", conn, err)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("err = %v, want context.DeadlineExceeded", err)
			}
			if d := time.Since(start); d > 2*time.Second {
				t.Fatalf("dial took %v; the dial deadline must bound the handshake", d)
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("a failed handshake must close its stream")
			}
		})
	}
}

func TestSOCKS5Dialer_CancelWithoutDeadlineUnblocks(t *testing.T) {
	dial := CreateSOCKS5Dialer(pipeLayer{egress: func(c net.Conn) {
		io.Copy(io.Discard, c)
	}})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)

	done := make(chan error, 1)
	go func() {
		_, err := dial(ctx, "tcp", "1.2.3.4:80")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the dial context must unblock the handshake")
	}
}

func TestSOCKS5Dialer_DomainBoundAddress(t *testing.T) {
	dial := CreateSOCKS5Dialer(pipeLayer{egress: func(c net.Conn) {
		answerGreeting(t, c)
		if readConnect(c) != nil {
			return
		}
		reply := []byte{0x05, 0x00, 0x00, 0x03, byte(len("egress.example"))}
		reply = append(reply, "egress.example"...)
		reply = append(reply, 0x01, 0xbb)
		c.Write(reply)
		c.Write([]byte("x"))
	}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", "1.2.3.4:443")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// The whole domain reply was consumed, so the next byte is payload.
	b := make([]byte, 1)
	if _, err := io.ReadFull(conn, b); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "x" {
		t.Fatalf("read %q, want %q: the domain reply was not fully consumed", b, "x")
	}
}

func TestSOCKS5Dialer_RejectsUnknownAddressType(t *testing.T) {
	dial := CreateSOCKS5Dialer(pipeLayer{egress: func(c net.Conn) {
		answerGreeting(t, c)
		if readConnect(c) != nil {
			return
		}
		c.Write([]byte{0x05, 0x00, 0x00, 0x09})
		io.Copy(io.Discard, c)
	}})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := dial(ctx, "tcp", "1.2.3.4:443")
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want an immediate unsupported-address-type error", err)
	}
}
