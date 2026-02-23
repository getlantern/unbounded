//go:build !wasm

// ui_default_impl.go implements the UI interface (see ui.go) for non-wasm build targets
package clientcore

import (
	"net"
)

type UIImpl struct {
	UI
	BroflakeEngine       *BroflakeEngine
	OnConnectionChangeFunc ConnectionChangeFunc
}

func (ui *UIImpl) Init(bf *BroflakeEngine) {
	ui.BroflakeEngine = bf
}

func (ui UIImpl) Start() {
	ui.BroflakeEngine.start()
}

func (ui UIImpl) Stop() {
	ui.BroflakeEngine.stop()
}

func (ui UIImpl) Debug() {
	ui.BroflakeEngine.debug()
}

func (ui UIImpl) OnReady() {
	// TODO: do something?
}

func (ui UIImpl) OnStartup() {
	ui.BroflakeEngine.start()
}

func (ui UIImpl) OnDownstreamChunk(size int, workerIdx int) {
	// TODO: do something?
}

func (ui UIImpl) OnDownstreamThroughput(bytesPerSec int) {
	// TODO: do something?
}

func (ui UIImpl) OnConsumerConnectionChange(state int, workerIdx int, addr net.IP) {
	if ui.OnConnectionChangeFunc != nil {
		ui.OnConnectionChangeFunc(state, workerIdx, addr)
	}
}
