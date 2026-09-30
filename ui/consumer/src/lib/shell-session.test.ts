import { describe, expect, test } from 'bun:test';
import type { ShellConnector } from './console-client/connector';
import type { ConsoleEnding } from './console-client/wasm-adapter';
import type { RawConsoleSession } from './console-sessions';
import { READY_TIMEOUT_MS, ShellSession, type ShellState } from './shell-session';

const PUBLIC_KEY = 'cd'.repeat(32);
const connection = {
  endpointID: 'ab'.repeat(32),
  relayURLs: ['https://relay.example'],
  target: 'agent.cell:8443',
};

function ready(reason: string, status = 'True', message?: string): RawConsoleSession {
  return {
    status: { connection, conditions: [{ type: 'Ready', status, reason, message }] },
  };
}

function harness(statuses: RawConsoleSession[], opts: { createError?: Error } = {}) {
  const states: ShellState[] = [];
  const output: Uint8Array[] = [];
  const removed: string[] = [];
  const created: unknown[] = [];
  const written: string[] = [];
  let disposed = 0;
  let clock = 0;
  let end: ((ending: ConsoleEnding) => void) | undefined;
  let emit: ((bytes: Uint8Array) => void) | undefined;
  let signalConnected: (() => void) | undefined;
  let connectedWith: unknown;

  const connector: ShellConnector = {
    load: async () => PUBLIC_KEY,
    connect(target, size, onOutput, onEnd, onConnected) {
      connectedWith = { target, size };
      emit = onOutput;
      end = onEnd;
      signalConnected = onConnected;
    },
    write: (data) => written.push(data),
    resize: () => undefined,
    dispose: () => {
      disposed++;
    },
  };

  const session = new ShellSession(
    {
      createConnector: () => connector,
      create: async (input) => {
        created.push(input);
        if (opts.createError) throw opts.createError;
        return { name: 'web-0-abcde', namespace: 'default', uid: 'session-uid' };
      },
      get: async () =>
        statuses.shift() ?? statuses[statuses.length - 1] ?? ready('Pending', 'Unknown'),
      remove: async (name) => {
        removed.push(name);
      },
      wait: async (ms) => {
        clock += ms;
      },
      now: () => clock,
    },
    {
      instanceName: 'web-0',
      instanceUid: 'instance-uid',
      onState: (state) => states.push(state),
      onOutput: (bytes) => output.push(bytes),
    }
  );

  return {
    session,
    states,
    output,
    removed,
    created,
    written,
    get disposed() {
      return disposed;
    },
    get connectedWith() {
      return connectedWith;
    },
    emit: (bytes: Uint8Array) => emit?.(bytes),
    connected: () => signalConnected?.(),
    end: (ending: Partial<ConsoleEnding>) =>
      end?.({ exitCode: 0, reason: '', message: '', error: '', ...ending }),
    open: () => session.open('app', () => ({ cols: 120, rows: 40 })),
  };
}

describe('ShellSession', () => {
  test('creates, waits for ready and connects with the session key', async () => {
    const h = harness([ready('Pending', 'Unknown'), ready('SessionReady')]);
    await h.open();

    expect(h.created).toEqual([
      {
        instanceName: 'web-0',
        instanceUid: 'instance-uid',
        containerName: 'app',
        clientPublicKey: PUBLIC_KEY,
      },
    ]);
    expect(h.states.map((s) => s.phase)).toEqual(['starting', 'waiting', 'connecting']);
    expect(h.connectedWith).toEqual({
      target: { uid: 'session-uid', ...connection },
      size: { cols: 120, rows: 40 },
    });

    h.connected();
    expect(h.states.at(-1)).toEqual({ phase: 'connected' });
    h.emit(new Uint8Array([36, 32]));
    expect(h.output).toHaveLength(1);

    h.session.write('ls\r');
    expect(h.written).toEqual(['ls\r']);
  });

  test('ends with the platform message and deletes the session', async () => {
    const h = harness([ready('NoShell', 'False', 'This container has no shell.')]);
    await h.open();

    expect(h.states.at(-1)).toEqual({ phase: 'ended', message: 'This container has no shell.' });
    expect(h.removed).toEqual(['web-0-abcde']);
    expect(h.disposed).toBe(1);
  });

  test('ends with the exit code when the shell exits', async () => {
    const h = harness([ready('SessionReady')]);
    await h.open();
    h.end({ exitCode: 2 });

    expect(h.states.at(-1)).toEqual({ phase: 'ended', message: 'The shell exited with code 2.' });
    expect(h.removed).toEqual(['web-0-abcde']);
  });

  test('shows the message for a platform ending on the stream', async () => {
    const h = harness([ready('SessionReady')]);
    await h.open();
    h.end({ exitCode: 1, reason: 'Expired', message: 'The session reached its 15 minute limit.' });

    expect(h.states.at(-1)).toEqual({
      phase: 'ended',
      message: 'The session reached its 15 minute limit.',
    });
  });

  test('explains a platform ending that carries no message', async () => {
    const h = harness([ready('SessionReady')]);
    await h.open();
    h.end({ exitCode: 1, reason: 'AgentShutdown' });

    expect(h.states.at(-1)).toMatchObject({
      phase: 'ended',
      message: expect.stringMatching(/maintenance/),
    });
  });

  test('reports a broken connection', async () => {
    const h = harness([ready('SessionReady')]);
    await h.open();
    h.end({ exitCode: 1, error: 'relay unreachable' });

    expect(h.states.at(-1)).toMatchObject({
      phase: 'ended',
      message: expect.stringMatching(/connection to the instance was lost \(relay unreachable\)/),
    });
    expect(h.removed).toEqual(['web-0-abcde']);
  });

  test('reports a clean exit', async () => {
    const h = harness([ready('SessionReady')]);
    await h.open();
    h.end({ exitCode: 0 });

    expect(h.states.at(-1)).toEqual({ phase: 'ended', message: 'The shell exited.' });
  });

  test('reports a create failure without leaving a session behind', async () => {
    const h = harness([], {
      createError: new Error('There are too many open sessions in this project.'),
    });
    await h.open();

    expect(h.states.at(-1)).toEqual({
      phase: 'ended',
      message: 'There are too many open sessions in this project.',
    });
    expect(h.removed).toEqual([]);
    expect(h.disposed).toBe(1);
  });

  test('gives up when the session never becomes ready', async () => {
    const h = harness([ready('Pending', 'Unknown')]);
    await h.open();

    expect(h.states.at(-1)).toMatchObject({ phase: 'ended' });
    expect(h.removed).toEqual(['web-0-abcde']);
    expect(READY_TIMEOUT_MS).toBeGreaterThan(0);
  });

  test('closing deletes the session, drops the client and ignores late output', async () => {
    const h = harness([ready('SessionReady')]);
    await h.open();
    h.session.close();

    expect(h.states.at(-1)).toEqual({ phase: 'idle' });
    expect(h.removed).toEqual(['web-0-abcde']);
    expect(h.disposed).toBe(1);

    h.emit(new Uint8Array([1]));
    h.end({ error: 'closed' });
    expect(h.output).toHaveLength(0);
    expect(h.states.at(-1)).toEqual({ phase: 'idle' });
  });

  test('disposing on navigation deletes the session', async () => {
    const h = harness([ready('SessionReady')]);
    await h.open();
    h.session.dispose();

    expect(h.removed).toEqual(['web-0-abcde']);
    expect(h.disposed).toBe(1);
  });

  test('stays connecting until the client reports the connection', async () => {
    const h = harness([ready('SessionReady')]);
    await h.open();
    h.emit(new Uint8Array([36]));
    expect(h.states.at(-1)).toEqual({ phase: 'connecting' });
  });

  test('ignores input before the connection opens', async () => {
    const h = harness([ready('NoShell', 'False')]);
    await h.open();
    h.session.write('x');
    expect(h.written).toEqual([]);
  });
});
