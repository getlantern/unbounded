//go:build wasm

// ui_wasm_impl.go implements the UI interface (see ui.go) for wasm build targets
package clientcore

import (
	"net"
	"strings"
	"syscall/js"

	"github.com/google/uuid"
)

type UIImpl struct {
	UI
	BroflakeEngine         *BroflakeEngine
	ID                     string
	OnConnectionChangeFunc ConnectionChangeFunc
}

func (ui *UIImpl) Init(bf *BroflakeEngine) {
	ui.BroflakeEngine = bf

	// The notion of 'ID' exists solely to avoid collisions in the JS namespace
	ui.ID = strings.Replace("L4NT3RN"+uuid.NewString(), "-", "", -1)

	// Construct the JavaScript API for this Broflake instance
	js.Global().Set(ui.ID, js.Global().Get("EventTarget").New())

	js.Global().Get(ui.ID).Set(
		"start",
		js.FuncOf(func(this js.Value, args []js.Value) interface{} { ui.Start(); return nil }),
	)

	js.Global().Get(ui.ID).Set(
		"stop",
		js.FuncOf(func(this js.Value, args []js.Value) interface{} { ui.Stop(); return nil }),
	)

	js.Global().Get(ui.ID).Set(
		"debug",
		js.FuncOf(func(this js.Value, args []js.Value) interface{} { ui.Debug(); return nil }),
	)
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

func (ui UIImpl) fireEvent(eventName string, detail map[string]interface{}) {
	options := map[string]interface{}{"detail": js.ValueOf(detail)}

	js.Global().Get(ui.ID).Call(
		"dispatchEvent",
		js.Global().Get("CustomEvent").New(js.ValueOf(eventName), js.ValueOf(options)),
	)
}

// 'ready' fires when the client is ready to be started with a call to 'start'
// after calling 'stop', you must wait for the 'ready' event before calling 'start' again!
func (ui UIImpl) OnReady() {
	ui.fireEvent("ready", map[string]interface{}{})
}

func (ui UIImpl) OnStartup() {
	// Do nothing
}

// 'downstreamChunk' fires once for each chunk of data received
// 'size' is the size of the chunk in bytes, 'workerIdx' is the 0-indexed ID of the connection slot
func (ui UIImpl) OnDownstreamChunk(size int, workerIdx int) {
	detail := map[string]interface{}{"size": size, "workerIdx": workerIdx}
	ui.fireEvent("downstreamChunk", detail)
}

// 'downstreamThroughput' fires N times per second, where N is determined by the uiRefreshHz
// hyperparameter. 'bytesPerSec is the current systemwide inbound throughput
func (ui UIImpl) OnDownstreamThroughput(bytesPerSec int) {
	detail := map[string]interface{}{"bytesPerSec": bytesPerSec}
	ui.fireEvent("downstreamThroughput", detail)
}

// 'consumerConnectionChange' fires when a consumer connects or disconnects. 'state' is 1 or -1,
// representing connection or disconnection; 'workerIdx' is the 0-indexed ID of the connection slot;
// 'addr' is a string which, when state == 1, represents the IPv4 or IPv6 address of the new
// consumer (or a 0-length string indicating that address extraction failed); when state == -1,
// addr == "<nil>"
func (ui UIImpl) OnConsumerConnectionChange(state int, workerIdx int, addr net.IP) {
	if ui.OnConnectionChangeFunc != nil {
		ui.OnConnectionChangeFunc(state, workerIdx, addr)
	}

	addrString := ""
	if addr != nil {
		addrString = addr.String()
	}

	detail := map[string]interface{}{"state": state, "workerIdx": workerIdx, "addr": addrString}
	ui.fireEvent("consumerConnectionChange", detail)
}
