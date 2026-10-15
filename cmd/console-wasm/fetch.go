//go:build js && wasm

package main

import (
	"strings"
	"syscall/js"
)

// refusePlainHTTPFetch makes the worker's fetch reject plain-HTTP URLs without
// sending them. The tunnel library's captive-portal check fetches the relay
// over plain HTTP with a custom header, which a browser blocks as mixed content
// or fails at the CORS preflight, logging an error on every connect. Its HTTPS
// latency probe is unaffected.
func refusePlainHTTPFetch() {
	global := js.Global()
	original := global.Get("fetch")
	if original.IsUndefined() {
		return
	}
	bound := original.Call("bind", global)
	global.Set("fetch", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) > 0 && args[0].Type() == js.TypeString && strings.HasPrefix(args[0].String(), "http:") {
			return global.Get("Promise").Call("reject",
				global.Get("TypeError").New("plain HTTP is not used from the console session client"))
		}
		return bound.Invoke(argsOf(args)...)
	}))
}

func argsOf(args []js.Value) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a
	}
	return out
}
