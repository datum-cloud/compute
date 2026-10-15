//go:build js && wasm

// Command console-wasm is the browser client for instance console sessions.
// See README.md for its JavaScript API.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"sync"
	"syscall/js"

	"go.datum.net/compute/internal/consoleclient"
	"go.datum.net/compute/internal/consolesession"
)

var (
	keyMu      sync.Mutex
	pendingKey ed25519.PrivateKey
)

func main() {
	refusePlainHTTPFetch()
	register()
	select {}
}

func register() {
	js.Global().Set("datumExec", js.ValueOf(map[string]any{
		"features":  []any{"connected", "end-after-zeroing"},
		"publicKey": js.FuncOf(publicKey),
		"connect":   js.FuncOf(connect),
	}))
}

func publicKey(js.Value, []js.Value) any {
	keyMu.Lock()
	defer keyMu.Unlock()
	if pendingKey == nil {
		_, k, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			panic(err)
		}
		pendingKey = k
	}
	return consolesession.PublicKey(pendingKey)
}

func takeKey() ed25519.PrivateKey {
	keyMu.Lock()
	defer keyMu.Unlock()
	k := pendingKey
	pendingKey = nil
	return k
}

func connect(_ js.Value, args []js.Value) any {
	if len(args) < 3 || len(args) > 4 {
		panic("datumExec.connect(session, onOutput, onEnd[, onConnected]) takes three or four arguments")
	}
	opts, onOutput, onEnd := args[0], args[1], args[2]
	h := consoleclient.Handlers{
		Output: func(p []byte, fd int) { onOutput.Invoke(toJS(p), fd) },
		End: func(res consoleclient.Result, err error) {
			ending := map[string]any{
				"exitCode": res.ExitCode,
				"reason":   res.Reason,
				"message":  res.Message,
				"error":    "",
			}
			if err != nil {
				ending["exitCode"] = 1
				ending["error"] = err.Error()
			}
			onEnd.Invoke(js.ValueOf(ending))
		},
	}
	if len(args) == 4 && args[3].Type() == js.TypeFunction {
		onConnected := args[3]
		h.Connected = func() { onConnected.Invoke() }
	}

	key := takeKey()
	if key == nil {
		go h.End(consoleclient.Result{}, errors.New("no session key: call datumExec.publicKey() before creating the session"))
		return handle(func([]byte) {}, func(int, int) {}, func() {})
	}

	s := consoleclient.NewSession(key, opts.Get("uid").String(), consoleclient.Connection{
		EndpointID:          opts.Get("endpointID").String(),
		RelayURLs:           stringSlice(opts.Get("relayURLs")),
		Target:              opts.Get("target").String(),
		AllowInsecureRelays: opts.Get("allowInsecureRelays").Truthy(),
	}, h)
	if cols, rows := opts.Get("cols"), opts.Get("rows"); cols.Type() == js.TypeNumber && rows.Type() == js.TypeNumber {
		s.Resize(cols.Int(), rows.Int())
	}
	go s.Run()

	return handle(s.Write, s.Resize, s.Close)
}

func handle(write func([]byte), resize func(cols, rows int), closeSession func()) js.Value {
	return js.ValueOf(map[string]any{
		"write": js.FuncOf(func(_ js.Value, a []js.Value) any {
			write(bytesOf(a[0]))
			return nil
		}),
		"resize": js.FuncOf(func(_ js.Value, a []js.Value) any {
			resize(a[0].Int(), a[1].Int())
			return nil
		}),
		"close": js.FuncOf(func(js.Value, []js.Value) any {
			closeSession()
			return nil
		}),
	})
}

func stringSlice(v js.Value) []string {
	if v.Type() != js.TypeObject {
		return nil
	}
	out := make([]string, v.Length())
	for i := range out {
		out[i] = v.Index(i).String()
	}
	return out
}

func bytesOf(v js.Value) []byte {
	if v.Type() == js.TypeString {
		return []byte(v.String())
	}
	b := make([]byte, v.Length())
	js.CopyBytesToGo(b, v)
	return b
}

func toJS(b []byte) js.Value {
	arr := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(arr, b)
	return arr
}
