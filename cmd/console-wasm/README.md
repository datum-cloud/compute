# Browser console session client

The browser build of the client that `datumctl compute exec` uses. It connects
a web terminal to an `InstanceConsoleSession` through the session's relays. The
browser cannot use direct paths, so traffic always goes through a relay.

## Build

```sh
make build-console-wasm
```

| Output | What it is |
|---|---|
| `bin/console-session.wasm` | The client, about 17 MB raw, 4.2 MB gzip, 2.9 MB brotli |
| `bin/console-session-wasm_exec.js` | Go's loader for it, from the same Go toolchain |

Serve both from the same build. A loader from another Go version does not work
with the module. Load the module when a terminal opens, not with the page, and
serve it compressed.

## Loading

Run it in a Web Worker so the key stays out of the page:

```js
importScripts('/console-session-wasm_exec.js');
const go = new Go();
const { instance } = await WebAssembly.instantiateStreaming(
  fetch('/console-session.wasm'),
  go.importObject,
);
go.run(instance);
// self.datumExec is now defined.
```

The module replaces the worker's `fetch` with one that refuses plain-HTTP
URLs, so the tunnel library's captive-portal check, which a browser always
blocks, never leaves the worker. The only probe the browser runs is the relays'
HTTPS latency probe, `GET /ping`, which the relays allow from any origin.

## API

The module defines one global, `datumExec`.

### `datumExec.publicKey()`

Returns the client's public key: 64 lowercase hexadecimal characters. Put it in
the session's `spec.clientPublicKey`.

The module generates the private key and never exposes it. It returns the same
key until `connect` uses it. After that, the next call returns a new key, so
each session gets its own.

### `datumExec.connect(session, onOutput, onEnd)`

Connects to a session that is ready, which means its `Ready` condition is
`True` with reason `SessionReady`. Connect right away: the session ends as
`NotConnected` at `status.connectBefore`.

`session` is an object with these fields:

| Field | Type | Value |
|---|---|---|
| `uid` | string | The session's `metadata.uid` |
| `endpointID` | string | `status.connection.endpointID` |
| `relayURLs` | string[] | `status.connection.relayURLs` |
| `target` | string | `status.connection.target` |
| `cols` | number | Optional. The terminal's width in columns |
| `rows` | number | Optional. The terminal's height in rows |

`onOutput(bytes, fd)` receives the command's output. `bytes` is a `Uint8Array`
and `fd` is `1` for standard output or `2` for standard error. A session with a
terminal sends everything on `1`.

`onEnd(ending)` is called exactly once. `ending` has these fields:

| Field | Type | Value |
|---|---|---|
| `exitCode` | number | The command's exit code, or `1` when `reason` or `error` is set |
| `reason` | string | Empty when the command exited. Otherwise the platform ended the session and this is its terminal reason, such as `Expired` |
| `message` | string | When `reason` is set, text to show the user |
| `error` | string | Set when the connection failed or broke, or `"closed"` after `close()` |

`connect` returns an object with these methods:

| Method | Effect |
|---|---|
| `write(data)` | Sends a string or `Uint8Array` to the command's standard input |
| `resize(cols, rows)` | Sets the terminal size |
| `close()` | Drops the connection. The platform stops the command |

The methods return at once; the client sends data in order in the background.

When `onEnd` fires, delete the session through the API. The client does not
call the API.

## Example

```js
const key = datumExec.publicKey();
// Create the session with spec.clientPublicKey = key and wait until it is
// ready, then:
const conn = datumExec.connect(
  {
    uid: s.metadata.uid,
    ...s.status.connection,
    cols: term.cols,
    rows: term.rows,
  },
  (bytes) => term.write(bytes),
  (end) => showEnding(end),
);
term.onData((d) => conn.write(d));
term.onResize(({ cols, rows }) => conn.resize(cols, rows));
```
