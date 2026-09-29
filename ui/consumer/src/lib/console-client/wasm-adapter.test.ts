import { afterEach, describe, expect, test } from 'bun:test';
import { loadConsoleClient } from './wasm-adapter';

const EMPTY_WASM_MODULE = new Uint8Array([0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00]);
const realFetch = globalThis.fetch;

afterEach(() => {
  globalThis.fetch = realFetch;
});

function fakeScope(endpointID: string) {
  const calls: unknown[][] = [];
  const scope: Record<string, unknown> = {};
  const datumExec = {
    publicKey: () => endpointID,
    connect(
      opts: unknown,
      onOutput: (b: Uint8Array, fd: number) => void,
      onEnd: (e: unknown) => void
    ) {
      calls.push(['connect', opts]);
      onOutput(new Uint8Array([104, 105]), 1);
      onEnd({ exitCode: 0 });
      return {
        write: (data: unknown) => calls.push(['write', data]),
        resize: (cols: number, rows: number) => calls.push(['resize', cols, rows]),
        close: () => calls.push(['close']),
      };
    },
  };
  function FakeGo(this: Record<string, unknown>) {
    this.importObject = {};
    this.run = () => {
      scope.datumExec = datumExec;
      return new Promise(() => undefined);
    };
  }
  scope.importScripts = (url: string) => {
    calls.push(['importScripts', url]);
    scope.Go = FakeGo;
  };
  return { scope, calls };
}

const assets = {
  wasmUrl: 'https://portal/console/c.wasm',
  runtimeUrl: 'https://portal/console/r.js',
};

describe('loadConsoleClient', () => {
  test('boots the wasm client and maps its API', async () => {
    globalThis.fetch = (async () => new Response(EMPTY_WASM_MODULE)) as unknown as typeof fetch;
    const { scope, calls } = fakeScope('ef'.repeat(32));

    const client = await loadConsoleClient(assets, scope as never);
    expect(client.publicKey()).toBe('ef'.repeat(32));

    const output: Uint8Array[] = [];
    const endings: unknown[] = [];
    const stream = client.connect(
      {
        uid: 'session-uid',
        endpointID: 'ab'.repeat(32),
        relayURLs: ['https://relay-1', 'https://relay-2'],
        target: 'agent:8443',
      },
      { cols: 100, rows: 30 },
      (bytes) => output.push(bytes),
      (ending) => endings.push(ending)
    );
    stream.write('ls');
    stream.resize({ cols: 90, rows: 20 });
    stream.close();

    expect(calls).toEqual([
      ['importScripts', assets.runtimeUrl],
      [
        'connect',
        {
          uid: 'session-uid',
          endpointID: 'ab'.repeat(32),
          relayURLs: ['https://relay-1', 'https://relay-2'],
          target: 'agent:8443',
          cols: 100,
          rows: 30,
        },
      ],
      ['write', 'ls'],
      ['resize', 90, 20],
      ['close'],
    ]);
    expect(output).toEqual([new Uint8Array([104, 105])]);
    expect(endings).toEqual([{ exitCode: 0, reason: '', message: '', error: '' }]);
  });

  test('rejects a key that is not 64 lowercase hex characters', async () => {
    globalThis.fetch = (async () => new Response(EMPTY_WASM_MODULE)) as unknown as typeof fetch;
    const { scope } = fakeScope('NOT-A-KEY');
    const client = await loadConsoleClient(assets, scope as never);
    expect(() => client.publicKey()).toThrow(/invalid key/);
  });

  test('reports a missing client download', async () => {
    globalThis.fetch = (async () => new Response('', { status: 404 })) as unknown as typeof fetch;
    const { scope } = fakeScope('ef'.repeat(32));
    await expect(loadConsoleClient(assets, scope as never)).rejects.toThrow(/404/);
  });
});
