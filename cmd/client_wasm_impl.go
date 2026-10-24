//go:build wasm

// client_wasm_impl.go is the entry point for standalone builds for wasm build targets
package main

import (
	"log/slog"
	"strings"
	"syscall/js"
	"time"

	"github.com/getlantern/broflake/clientcore"
)

// widgetNATFailTimeout is how long a browser volunteer waits, once signaling completes, for the
// data channel to open. clientcore's 5s default is too short for the widget: of the first 94
// outcomes volunteers reported to freddie (freddie.producer.outcomes, 2026-10-24), 70 were
// nat_timeout with ICE already connected and the peer connection still connecting. NAT traversal
// had worked and the DTLS/SCTP handshake was still running, at ~0.6s per round trip to a distant
// consumer, when the volunteer gave up.
const widgetNATFailTimeout = 15 * time.Second

func main() {
	slog.Debug("wasm client started...")

	// A constructor is exposed to JS. Some (but not all) defaults are forcibly overridden by passing
	// args. You *must* pass valid values for all of these args:
	//
	// newBroflake(
	//    BroflakeOptions.ClientType,
	//    BroflakeOptions.CTableSize,
	//    BroflakeOptions.PTableSize,
	//    BroflakeOptions.BusBufferSz,
	//    BroflakeOptions.Netstated,
	//    WebRTCOptions.DiscoverySrv
	//    WebRTCOptions.Endpoint,
	//    WebRTCOptions.STUNBatchSize,
	//    WebRTCOptions.Tag
	//    EgressOptions.Addr
	//    EgressOptions.Endpoint
	//    [WebRTCOptions.TargetCountries] (optional, comma-separated, e.g. "IR" or "IR,RU")
	// )
	//
	// The target countries can also be changed later via the returned API's setTargetCountries().
	//
	// Returns a reference to a Broflake JS API impl (defined in ui_wasm_impl.go)
	js.Global().Set(
		"newBroflake",
		js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			bfOpt := clientcore.BroflakeOptions{
				ClientType:  args[0].String(),
				CTableSize:  args[1].Int(),
				PTableSize:  args[2].Int(),
				BusBufferSz: args[3].Int(),
				Netstated:   args[4].String(),
			}

			rtcOpt := clientcore.NewDefaultWebRTCOptions()
			rtcOpt.DiscoverySrv = args[5].String()
			rtcOpt.Endpoint = args[6].String()
			rtcOpt.STUNBatchSize = uint32(args[7].Int())
			rtcOpt.Tag = args[8].String()
			rtcOpt.NATFailTimeout = widgetNATFailTimeout
			if len(args) > 11 && args[11].Type() == js.TypeString {
				rtcOpt.SetTargetCountries(strings.Split(args[11].String(), ","))
			}

			egOpt := clientcore.NewDefaultEgressOptions()
			egOpt.Addr = args[9].String()
			egOpt.Endpoint = args[10].String()

			_, ui, err := clientcore.NewBroflake(&bfOpt, rtcOpt, egOpt)
			if err != nil {
				slog.Debug("newBroflake error", "error", err)
				return nil
			}
			slog.Debug("Built new Broflake API", "id", ui.ID)
			return js.Global().Get(ui.ID)
		}),
	)

	select {}
}
