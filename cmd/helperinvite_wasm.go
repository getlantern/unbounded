//go:build wasm

package main

import (
	"crypto/rand"
	"syscall/js"

	"github.com/getlantern/broflake/common/helperinvite"
)

// installHelperInvite exposes the helper-invite primitives the landing page
// needs to volunteer from a browser. Each function returns a string or an
// object; on failure, an object with "error" set to a fixed message. Nothing
// here logs: the arguments are secrets and names.
func installHelperInvite() {
	str := func(args []js.Value, i int) string {
		if i < len(args) && args[i].Type() == js.TypeString {
			return args[i].String()
		}
		return ""
	}
	fail := func(err error) interface{} {
		return js.ValueOf(map[string]interface{}{"error": err.Error()})
	}
	api := map[string]interface{}{
		"newSeed": js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			s, err := helperinvite.BrowserNewSeed(rand.Reader)
			if err != nil {
				return fail(err)
			}
			return s
		}),
		"claim": js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			body, code, err := helperinvite.BrowserClaim(str(args, 0), str(args, 1), str(args, 2), rand.Reader)
			if err != nil {
				return fail(err)
			}
			return js.ValueOf(map[string]interface{}{"body": body, "code": code})
		}),
		"matchingCode": js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			code, err := helperinvite.BrowserMatchingCode(str(args, 0), str(args, 1))
			if err != nil {
				return fail(err)
			}
			return code
		}),
		"openInviterName": js.FuncOf(func(this js.Value, args []js.Value) interface{} {
			name, err := helperinvite.BrowserOpenInviterName(str(args, 0), str(args, 1))
			if err != nil {
				return fail(err)
			}
			return name
		}),
	}
	js.Global().Set("lanternHelperInvite", js.ValueOf(api))
}
