import { describe, expect, test } from 'bun:test';
import { createWorkerConnector, type ShellWorker } from './connector';
import type { FromWorker, ToWorker } from './messages';

const assets = { wasmUrl: 'c.wasm', runtimeUrl: 'r.js' };
const target = { uid: 'u', endpointID: 'e', relayURLs: ['https://relay'], target: 't' };
const size = { cols: 80, rows: 24 };

function fakeWorker(respond: (message: ToWorker, reply: (m: FromWorker) => void) => void) {
  const sent: ToWorker[] = [];
  let terminated = 0;
  const worker: ShellWorker = {
    onmessage: null,
    onerror: null,
    postMessage(message) {
      sent.push(message);
      respond(message, (reply) => worker.onmessage?.({ data: reply } as MessageEvent<FromWorker>));
    },
    terminate() {
      terminated++;
    },
  };
  return {
    worker,
    sent,
    get terminated() {
      return terminated;
    },
  };
}

const ending = { exitCode: 1, reason: '', message: '', error: 'closed' };

describe('createWorkerConnector', () => {
  test('relays the connected signal', () => {
    const fake = fakeWorker((m, reply) => {
      if (m.type === 'connect') reply({ type: 'connected' });
    });
    const connector = createWorkerConnector(assets, () => fake.worker);
    const events: string[] = [];
    connector.connect(
      target,
      size,
      () => events.push('output'),
      () => events.push('end'),
      () => events.push('connected')
    );
    expect(events).toEqual(['connected']);
  });

  test('closes the client and waits for it to end before stopping the worker', () => {
    const fake = fakeWorker((m, reply) => {
      if (m.type === 'close') reply({ type: 'end', ending });
    });
    const connector = createWorkerConnector(assets, () => fake.worker);
    const endings: unknown[] = [];
    connector.connect(
      target,
      size,
      () => undefined,
      (e) => endings.push(e),
      () => undefined
    );

    connector.dispose();

    expect(fake.sent.map((m) => m.type)).toEqual(['connect', 'close']);
    expect(fake.terminated).toBe(1);
    expect(endings).toEqual([]);
  });

  test('stops the worker after the timeout when the client never ends', async () => {
    const fake = fakeWorker(() => undefined);
    const connector = createWorkerConnector(assets, () => fake.worker, 5);
    connector.connect(
      target,
      size,
      () => undefined,
      () => undefined,
      () => undefined
    );

    connector.dispose();
    expect(fake.terminated).toBe(0);
    await new Promise((resolve) => setTimeout(resolve, 20));
    expect(fake.terminated).toBe(1);
  });

  test('stops the worker at once when no connection is open', () => {
    const fake = fakeWorker(() => undefined);
    const connector = createWorkerConnector(assets, () => fake.worker);
    connector.dispose();
    expect(fake.sent).toEqual([]);
    expect(fake.terminated).toBe(1);
  });

  test('stops the worker at once when the connection already ended', () => {
    const fake = fakeWorker((m, reply) => {
      if (m.type === 'connect') reply({ type: 'end', ending });
    });
    const connector = createWorkerConnector(assets, () => fake.worker);
    connector.connect(
      target,
      size,
      () => undefined,
      () => undefined,
      () => undefined
    );
    connector.dispose();
    expect(fake.sent.map((m) => m.type)).toEqual(['connect']);
    expect(fake.terminated).toBe(1);
  });
});
