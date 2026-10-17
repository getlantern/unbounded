//go:build !wasm

package clientcore

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/getlantern/broflake/common"
)

func TestUpstreamUIHandlerBatchesBytesPerWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	got := make(map[int]int)
	calls := 0
	ui := UIImpl{OnUpstreamChunkFunc: func(size, workerIdx int) {
		mu.Lock()
		defer mu.Unlock()
		got[workerIdx] += size
		calls++
	}}

	handle := UpstreamUIHandler(ctx, ui, "", "")
	for i := 0; i < 10; i++ {
		handle(IPCMsg{IpcType: ChunkIPC, Data: make([]byte, 100), Wid: 0})
	}
	handle(IPCMsg{IpcType: ChunkIPC, Data: make([]byte, 7), Wid: 3})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := got[0] == 1000 && got[3] == 7
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if got[0] != 1000 || got[3] != 7 {
		t.Fatalf("expected 1000 bytes on worker 0 and 7 on worker 3, got %v", got)
	}
	if calls > 4 {
		t.Fatalf("expected per-worker batching, got %d callbacks for 11 chunks", calls)
	}
}

func TestDownstreamChunkForwardsToCallback(t *testing.T) {
	var size, wid int
	ui := UIImpl{OnDownstreamChunkFunc: func(s, w int) { size, wid = s, w }}
	ui.OnDownstreamChunk(42, 5)
	if size != 42 || wid != 5 {
		t.Fatalf("expected (42, 5), got (%d, %d)", size, wid)
	}
}

func TestUpstreamUIHandlerFlushesPendingOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	got := make(chan int, 1)
	ui := UIImpl{OnUpstreamChunkFunc: func(size, _ int) { got <- size }}

	handle := UpstreamUIHandler(ctx, ui, "", "")
	handle(IPCMsg{IpcType: ChunkIPC, Data: make([]byte, 64), Wid: 1})
	cancel()

	select {
	case size := <-got:
		if size != 64 {
			t.Fatalf("expected 64 pending bytes, got %d", size)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending upstream bytes were dropped on cancel")
	}
}

func TestUpstreamUIHandlerDeliversPendingBeforeConnectionChange(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var mu sync.Mutex
	var events []string
	ui := UIImpl{
		OnUpstreamChunkFunc: func(size, _ int) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, "chunk")
		},
		OnConnectionChangeFunc: func(_ int, _ int, _ net.IP) {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, "conn")
		},
	}

	handle := UpstreamUIHandler(ctx, ui, "", "")
	handle(IPCMsg{IpcType: ChunkIPC, Data: make([]byte, 10), Wid: 2})
	handle(IPCMsg{IpcType: ConsumerInfoIPC, Data: common.ConsumerInfo{}, Wid: 2})
	defer connectedConsumers.set(2, common.ConsumerInfo{})

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[0] != "chunk" || events[1] != "conn" {
		t.Fatalf("expected pending bytes delivered before the connection change, got %v", events)
	}
}
