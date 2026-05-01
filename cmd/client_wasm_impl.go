//go:build wasm

// client_wasm_impl.go is the entry point for standalone builds for wasm build targets
package main

import (
	"fmt"
	"log/slog"
	"syscall/js"

	"github.com/getlantern/broflake/clientcore"
)

func main() {
	slog.Debug(fmt.Sprintf("wasm client started..."))

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
	// )
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

			egOpt := clientcore.NewDefaultEgressOptions()
			egOpt.Addr = args[9].String()
			egOpt.Endpoint = args[10].String()

			_, ui, err := clientcore.NewBroflake(&bfOpt, rtcOpt, egOpt)
			if err != nil {
				slog.Debug(fmt.Sprintf("newBroflake error: %v", err))
				return nil
			}
			slog.Debug(fmt.Sprintf("Built new Broflake API: %v", ui.ID))
			return js.Global().Get(ui.ID)
		}),
	)

	select {}
}
