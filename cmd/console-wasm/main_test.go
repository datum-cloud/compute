//go:build js && wasm

package main

import (
	"strings"
	"syscall/js"
	"testing"
	"time"
)

type callbacks struct {
	connected chan struct{}
	ended     chan js.Value
	funcs     []js.Func
}

func newCallbacks(t *testing.T) *callbacks {
	c := &callbacks{connected: make(chan struct{}, 1), ended: make(chan js.Value, 2)}
	t.Cleanup(func() {
		for _, f := range c.funcs {
			f.Release()
		}
	})
	return c
}

func (c *callbacks) fn(f func(args []js.Value)) js.Func {
	jf := js.FuncOf(func(_ js.Value, args []js.Value) any {
		f(args)
		return nil
	})
	c.funcs = append(c.funcs, jf)
	return jf
}

func session(relay string) js.Value {
	return js.ValueOf(map[string]any{
		"uid":        "0b6e4c8e-3a6f-4c55-9d7e-7f4c1d2a9e10",
		"endpointID": strings.Repeat("a", 64),
		"relayURLs":  []any{relay},
		"target":     "agent:7777",
	})
}

func (c *callbacks) awaitEnd(t *testing.T) js.Value {
	t.Helper()
	select {
	case e := <-c.ended:
		return e
	case <-time.After(5 * time.Second):
		t.Fatal("onEnd never called")
		return js.Undefined()
	}
}

func TestFeatures(t *testing.T) {
	register()
	features := js.Global().Get("datumExec").Get("features")
	var got []string
	for i := 0; i < features.Length(); i++ {
		got = append(got, features.Index(i).String())
	}
	if strings.Join(got, ",") != "connected,end-after-zeroing" {
		t.Errorf("features = %q", got)
	}
}

func TestConnectArguments(t *testing.T) {
	register()
	exec := js.Global().Get("datumExec")
	for _, withConnected := range []bool{false, true} {
		c := newCallbacks(t)
		exec.Call("publicKey")
		args := []any{
			session("http://relay.example.com"),
			c.fn(func([]js.Value) {}),
			c.fn(func(a []js.Value) { c.ended <- a[0] }),
		}
		if withConnected {
			args = append(args, c.fn(func([]js.Value) { c.connected <- struct{}{} }))
		}
		conn := exec.Call("connect", args...)
		for _, m := range []string{"write", "resize", "close"} {
			if conn.Get(m).Type() != js.TypeFunction {
				t.Errorf("connect() result has no %s()", m)
			}
		}

		end := c.awaitEnd(t)
		if end.Get("exitCode").Int() != 1 || !strings.Contains(end.Get("error").String(), "not encrypted") {
			t.Errorf("onEnd(%v) with %d arguments, want the plain relay refused",
				js.Global().Get("JSON").Call("stringify", end), len(args))
		}
		select {
		case <-c.connected:
			t.Error("onConnected called for a session that never connected")
		default:
		}
	}
}

func TestConnectWithoutKey(t *testing.T) {
	register()
	takeKey()
	c := newCallbacks(t)
	js.Global().Get("datumExec").Call("connect", session("https://relay.example.com"),
		c.fn(func([]js.Value) {}), c.fn(func(a []js.Value) { c.ended <- a[0] }))
	if end := c.awaitEnd(t); !strings.Contains(end.Get("error").String(), "no session key") {
		t.Errorf("onEnd error = %q, want the missing key reported", end.Get("error").String())
	}
}

func TestPlainHTTPFetchRefused(t *testing.T) {
	refusePlainHTTPFetch()
	c := newCallbacks(t)
	result := make(chan string, 1)
	js.Global().Call("fetch", "http://relay.example.com/generate_204").Call("then",
		c.fn(func([]js.Value) { result <- "fulfilled" }),
		c.fn(func(a []js.Value) { result <- "rejected: " + a[0].Get("message").String() }))
	select {
	case got := <-result:
		if !strings.HasPrefix(got, "rejected") {
			t.Errorf("plain HTTP fetch %s, want it refused", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("fetch never settled")
	}
}
