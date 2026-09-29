//go:build js && wasm

// Command console-wasm is the browser client for instance console sessions.
// See README.md for its JavaScript API.
package main

import (
	"context"
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
	js.Global().Set("datumExec", js.ValueOf(map[string]any{
		"publicKey": js.FuncOf(publicKey),
		"connect":   js.FuncOf(connect),
	}))
	select {}
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
	if len(args) != 3 {
		panic("datumExec.connect(session, onOutput, onEnd) takes three arguments")
	}
	opts := args[0]
	c := &conn{
		key:        takeKey(),
		sessionUID: opts.Get("uid").String(),
		connection: consoleclient.Connection{
			EndpointID: opts.Get("endpointID").String(),
			RelayURLs:  stringSlice(opts.Get("relayURLs")),
			Target:     opts.Get("target").String(),
		},
		onOutput: args[1],
		onEnd:    args[2],
		wake:     make(chan struct{}, 1),
	}
	if cols, rows := opts.Get("cols"), opts.Get("rows"); cols.Type() == js.TypeNumber && rows.Type() == js.TypeNumber {
		c.queue = append(c.queue, resizeOp(cols.Int(), rows.Int()))
	}
	c.ctx, c.cancel = context.WithCancel(context.Background())
	go c.run()

	return js.ValueOf(map[string]any{
		"write": js.FuncOf(func(_ js.Value, a []js.Value) any {
			data := bytesOf(a[0])
			c.enqueue(func(s *consoleclient.Stream) error {
				_, err := s.Write(data)
				return err
			})
			return nil
		}),
		"resize": js.FuncOf(func(_ js.Value, a []js.Value) any {
			c.enqueue(resizeOp(a[0].Int(), a[1].Int()))
			return nil
		}),
		"close": js.FuncOf(func(js.Value, []js.Value) any {
			c.cancel()
			return nil
		}),
	})
}

type op func(*consoleclient.Stream) error

func resizeOp(cols, rows int) op {
	return func(s *consoleclient.Stream) error {
		return s.Resize(uint16(cols), uint16(rows))
	}
}

// conn queues writes from JavaScript so its callbacks never block on the
// network, which in WebAssembly needs the event loop they would be holding.
type conn struct {
	key        ed25519.PrivateKey
	sessionUID string
	connection consoleclient.Connection
	onOutput   js.Value
	onEnd      js.Value

	ctx    context.Context
	cancel context.CancelFunc

	mu    sync.Mutex
	queue []op
	wake  chan struct{}
	ended sync.Once
}

func (c *conn) enqueue(o op) {
	c.mu.Lock()
	c.queue = append(c.queue, o)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *conn) run() {
	defer c.cancel()
	if c.key == nil {
		c.end(consoleclient.Result{}, errors.New("no session key: call datumExec.publicKey() before creating the session"))
		return
	}
	defer clear(c.key)

	s, err := consoleclient.Connect(c.ctx, c.key, c.sessionUID, c.connection)
	if err != nil {
		c.end(consoleclient.Result{}, err)
		return
	}
	go func() {
		<-c.ctx.Done()
		_ = s.Close()
	}()
	go c.drain(s)

	res, err := s.Wait(writerFunc(func(p []byte) { c.onOutput.Invoke(toJS(p), 1) }),
		writerFunc(func(p []byte) { c.onOutput.Invoke(toJS(p), 2) }))
	if err != nil && c.ctx.Err() != nil {
		err = errors.New("closed")
	}
	c.end(res, err)
}

func (c *conn) drain(s *consoleclient.Stream) {
	for {
		c.mu.Lock()
		ops := c.queue
		c.queue = nil
		c.mu.Unlock()
		for _, o := range ops {
			if err := o(s); err != nil {
				return
			}
		}
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
		}
	}
}

func (c *conn) end(res consoleclient.Result, err error) {
	c.ended.Do(func() {
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
		c.onEnd.Invoke(js.ValueOf(ending))
	})
}

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) {
	f(p)
	return len(p), nil
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
