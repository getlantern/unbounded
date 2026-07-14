// ipc.go defines structures and functionality for communication between client system components
package clientcore

import "context"

// ChunkIPC: data plane traffic
// PathAssertionIPC: how upstream processes describe their connectivity for downstream processes
// ConsumerInfoIPC: how downstream processes describe their connectivity for upstream processes
// ConnectivityCheckIPC: how processes request the connectivity situation from their counterparts

const (
	ChunkIPC msgType = iota
	PathAssertionIPC
	ConsumerInfoIPC
	ConnectivityCheckIPC
)

const (
	NoRoute        = workerID(-1)
	BroadcastRoute = workerID(-2)
)

type msgType int
type workerID int

type IPCMsg struct {
	IpcType msgType
	Data    interface{}
	Wid     workerID
}

type ipcChan struct {
	tx chan IPCMsg
	rx chan IPCMsg
}

func newIpcChan(bufferSz int) *ipcChan {
	return &ipcChan{tx: make(chan IPCMsg, bufferSz), rx: make(chan IPCMsg, bufferSz)}
}

type ipcObserver struct {
	Downstream *ipcChan
	Upstream   *ipcChan
	onTx       func(IPCMsg)
	onRx       func(IPCMsg)
}

func forwardIPC(ctx context.Context, src <-chan IPCMsg, dst chan<- IPCMsg, hook func(IPCMsg)) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-src:
			if !ok {
				return
			}

			hook(msg)

			select {
			case <-ctx.Done():
				return
			case dst <- msg:
			}
		}
	}
}

func (o *ipcObserver) Start(ctx context.Context) {
	go forwardIPC(ctx, o.Downstream.tx, o.Upstream.tx, o.onTx)
	go forwardIPC(ctx, o.Upstream.rx, o.Downstream.rx, o.onRx)
}

func NewIpcObserver(bufferSz int, onTx, onRx func(IPCMsg)) *ipcObserver {
	if onTx == nil {
		onTx = func(IPCMsg) {}
	}

	if onRx == nil {
		onRx = func(IPCMsg) {}
	}

	return &ipcObserver{
		Downstream: newIpcChan(bufferSz),
		Upstream:   newIpcChan(bufferSz),
		onTx:       onTx,
		onRx:       onRx,
	}
}
