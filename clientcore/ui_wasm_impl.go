//go:build wasm

// ui_wasm_impl.go implements the UI interface (see ui.go) for wasm build targets
package clientcore

import (
	"log/slog"
	"net"
	"strings"
	"syscall/js"

	"github.com/getlantern/broflake/common"
	"github.com/google/uuid"
)

type UIImpl struct {
	UI
	BroflakeEngine             *BroflakeEngine
	ID                         string
	OnConnectionChangeFunc     ConnectionChangeFunc
	OnConnectionChangeInfoFunc ConnectionChangeInfoFunc
	OnDownstreamChunkFunc      ChunkFunc
	OnUpstreamChunkFunc        ChunkFunc
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

	// setTargetCountries("IR") or setTargetCountries("IR,RU") names the countries whose consumers
	// this widget prefers to serve; "" clears the preference. Safe to call at any time: each
	// worker picks it up with its next genesis message.
	js.Global().Get(ui.ID).Set(
		"setTargetCountries",
		js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			csv := ""
			if len(args) > 0 && args[0].Type() == js.TypeString {
				csv = args[0].String()
			}
			ui.BroflakeEngine.setTargetCountries(strings.Split(csv, ","))
			return nil
		}),
	)

	// setRoomTickets(["...", "..."]) or setRoomTickets("...,...") gives the
	// widget its helper pairings' volunteer tickets (from POST
	// /v1/helper/tickets). setRoomTickets([]) leaves every room, and so does
	// any argument it does not recognise, on purpose: see roomTicketsArg.
	// Safe to call at any time. Nothing here logs: tickets are credentials.
	js.Global().Get(ui.ID).Set(
		"setRoomTickets",
		js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			ui.BroflakeEngine.setRoomTickets(roomTicketsArg(jsArg(args, 0)))
			return nil
		}),
	)

	// setServePool(true) also serves everyone ("help others too");
	// setServePool(false), or anything but a boolean true, limits the widget
	// to its pairings' rooms: see servePoolArg.
	js.Global().Get(ui.ID).Set(
		"setServePool",
		js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			ui.BroflakeEngine.setServePool(servePoolArg(jsArg(args, 0)))
			return nil
		}),
	)

	// Exposes liveness() so the page can tell a wedged Go runtime apart from a
	// wedged browser main thread. See watchdog_wasm_impl.go for why that
	// distinction is the one that matters.
	installWatchdog(ui.ID)
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
	slog.Debug("Unbounded", "version", common.Version)
}

// 'downstreamChunk' fires once for each chunk of data received
// 'size' is the size of the chunk in bytes, 'workerIdx' is the 0-indexed ID of the connection slot
func (ui UIImpl) OnDownstreamChunk(size int, workerIdx int) {
	if ui.OnDownstreamChunkFunc != nil {
		ui.OnDownstreamChunkFunc(size, workerIdx)
	}
	detail := map[string]interface{}{"size": size, "workerIdx": workerIdx}
	ui.fireEvent("downstreamChunk", detail)
}

// 'downstreamThroughput' fires N times per second, where N is determined by the uiRefreshHz
// hyperparameter. 'bytesPerSec is the current systemwide inbound throughput
func (ui UIImpl) OnDownstreamThroughput(bytesPerSec int) {
	detail := map[string]interface{}{"bytesPerSec": bytesPerSec}
	ui.fireEvent("downstreamThroughput", detail)
}

// 'upstreamChunk' fires up to uiRefreshHz times per second for each connection slot that sent
// data upstream since the last event. 'size' is the number of bytes sent since then, 'workerIdx'
// is the 0-indexed ID of the connection slot
func (ui UIImpl) OnUpstreamChunk(size int, workerIdx int) {
	if ui.OnUpstreamChunkFunc != nil {
		ui.OnUpstreamChunkFunc(size, workerIdx)
	}
	detail := map[string]interface{}{"size": size, "workerIdx": workerIdx}
	ui.fireEvent("upstreamChunk", detail)
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

// jsArg converts args[i] for the native-tested argument mappings in
// ui_args.go: a boolean, string or number to bool, string or float64, an
// array to []any of those, and anything else, including a missing argument,
// undefined and null, to nil.
func jsArg(args []js.Value, i int) any {
	if i >= len(args) {
		return nil
	}
	v := args[i]
	if v.Type() == js.TypeObject && js.Global().Get("Array").Call("isArray", v).Bool() {
		out := make([]any, v.Length())
		for j := range out {
			out[j] = jsScalar(v.Index(j))
		}
		return out
	}
	return jsScalar(v)
}

func jsScalar(v js.Value) any {
	switch v.Type() {
	case js.TypeBoolean:
		return v.Bool()
	case js.TypeString:
		return v.String()
	case js.TypeNumber:
		return v.Float()
	}
	return nil
}
