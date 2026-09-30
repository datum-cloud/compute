import { describe, expect, test } from 'bun:test';
import type { ShellConnector } from './console-client/connector';
import type { ConsoleEnding } from './console-client/wasm-adapter';
import { ApiError } from './api';
import type { RawConsoleSession, SessionEnding, SessionWatchEvent } from './console-sessions';
import { SESSION_GONE_MESSAGE } from './console-sessions';
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

type WatchScript = Array<SessionWatchEvent | 'break'>;

function harness(
  watches: WatchScript[],
  opts: {
    createError?: Error;
    initial?: RawConsoleSession;
    gets?: Array<RawConsoleSession | Error>;
    ending?: SessionEnding;
  } = {}
) {
  const states: ShellState[] = [];
  const output: Uint8Array[] = [];
  const removed: string[] = [];
  const created: unknown[] = [];
  const written: string[] = [];
  const watchedFrom: string[] = [];
  const endingsAsked: string[] = [];
  let disposed = 0;
  let clock = 0;
  let expire: (() => void) | undefined;
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
        return {
          name: 'web-0-abcde',
          namespace: 'default',
          uid: 'session-uid',
          resourceVersion: '1',
          initial: opts.initial ?? ready('Pending', 'Unknown'),
        };
      },
      watch: async function* (_name, resourceVersion) {
        watchedFrom.push(resourceVersion);
        for (const step of watches.shift() ?? []) {
          if (step === 'break') throw new Error('stream reset');
          yield step;
        }
      },
      get: async () => {
        const next = opts.gets?.shift() ?? ready('Pending', 'Unknown');
        if (next instanceof Error) throw next;
        return next;
      },
      ending: async (uid) => {
        endingsAsked.push(uid);
        return opts.ending;
      },
      remove: async (name) => {
        removed.push(name);
      },
      wait: (ms, signal) => {
        if (signal) {
          return new Promise<void>((resolve) => {
            expire = resolve;
          });
        }
        clock += ms;
        return Promise.resolve();
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
    watchedFrom,
    endingsAsked,
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
    expire: () => expire?.(),
    open: () => session.open('app', () => ({ cols: 120, rows: 40 })),
  };
}

const settle = () => new Promise((resolve) => setTimeout(resolve, 0));

function modified(raw: RawConsoleSession, resourceVersion = '2'): SessionWatchEvent {
  return {
    type: 'MODIFIED',
    object: { ...raw, metadata: { name: 'web-0-abcde', resourceVersion } },
  };
}

function deleted(raw: RawConsoleSession): SessionWatchEvent {
  return { type: 'DELETED', object: { ...raw, metadata: { name: 'web-0-abcde' } } };
}

describe('ShellSession', () => {
  test('creates, watches until ready and connects with the session key', async () => {
    const h = harness([
      [modified(ready('Pending', 'Unknown')), modified(ready('SessionReady'), '3')],
    ]);
    await h.open();

    expect(h.created).toEqual([
      {
        instanceName: 'web-0',
        instanceUid: 'instance-uid',
        containerName: 'app',
        clientPublicKey: PUBLIC_KEY,
      },
    ]);
    expect(h.watchedFrom).toEqual(['1']);
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

  test('connects at once when the created session is already ready', async () => {
    const h = harness([], { initial: ready('SessionReady') });
    await h.open();

    expect(h.watchedFrom).toEqual([]);
    expect(h.states.at(-1)).toEqual({ phase: 'connecting' });
  });

  test('explains a session that ended with no shell, and deletes it', async () => {
    const h = harness([[modified(ready('NoShell', 'False', 'The container has no shell (sh).'))]]);
    await h.open();

    expect(h.states.at(-1)).toEqual({
      phase: 'ended',
      message: expect.stringMatching(
        /^This container has no shell \(sh\), so a shell session can't start\./
      ),
    });
    expect(h.removed).toEqual(['web-0-abcde']);
    expect(h.disposed).toBe(1);
  });

  test('reads the ending from the deletion when the session is removed as it ends', async () => {
    const h = harness([[deleted(ready('NoShell', 'False'))]]);
    await h.open();

    expect(h.states.at(-1)).toMatchObject({
      phase: 'ended',
      message: expect.stringMatching(/no shell \(sh\)/),
    });
    expect(h.endingsAsked).toEqual([]);
  });

  test('recovers the ending from the session events once the session is gone', async () => {
    const h = harness([['break']], {
      gets: [new ApiError(404, 'The session could not be read (404).')],
      ending: { reason: 'NoShell' },
    });
    await h.open();

    expect(h.endingsAsked).toEqual(['session-uid']);
    expect(h.states.at(-1)).toMatchObject({
      phase: 'ended',
      message: expect.stringMatching(/no shell \(sh\)/),
    });
    expect(JSON.stringify(h.states)).not.toContain('404');
  });

  test('says so plainly when a gone session left no ending behind', async () => {
    const h = harness([[deleted(ready('Pending', 'Unknown'))]]);
    await h.open();

    expect(h.states.at(-1)).toEqual({ phase: 'ended', message: SESSION_GONE_MESSAGE });
  });

  test('resumes a broken watch from the session it reads again', async () => {
    const h = harness([['break'], [modified(ready('SessionReady'), '8')]], {
      gets: [{ ...ready('Pending', 'Unknown'), metadata: { resourceVersion: '7' } }],
    });
    await h.open();

    expect(h.watchedFrom).toEqual(['1', '7']);
    expect(h.states.at(-1)).toEqual({ phase: 'connecting' });
  });

  test('names the missing command', async () => {
    const h = harness([[modified(ready('CommandUnavailable', 'False'))]]);
    await h.open();

    expect(h.states.at(-1)).toEqual({
      phase: 'ended',
      message: "sh isn't installed in this container.",
    });
  });

  test('ends with the exit code when the shell exits', async () => {
    const h = harness([], { initial: ready('SessionReady') });
    await h.open();
    h.end({ exitCode: 2 });
    await settle();

    expect(h.states.at(-1)).toEqual({ phase: 'ended', message: 'The shell exited with code 2.' });
    expect(h.removed).toEqual(['web-0-abcde']);
  });

  test('explains a platform ending on the stream', async () => {
    const h = harness([], { initial: ready('SessionReady') });
    await h.open();
    h.end({ exitCode: 1, reason: 'AgentShutdown' });
    await settle();

    expect(h.states.at(-1)).toMatchObject({
      phase: 'ended',
      message: expect.stringMatching(/maintenance/),
    });
  });

  test('explains a broken stream from the session status', async () => {
    const h = harness([], {
      initial: ready('SessionReady'),
      gets: [ready('AgentLost', 'False')],
    });
    await h.open();
    h.end({ exitCode: 1, error: 'relay unreachable' });
    await settle();

    expect(h.states.at(-1)).toMatchObject({
      phase: 'ended',
      message: expect.stringMatching(/lost contact with the session/),
    });
    expect(h.removed).toEqual(['web-0-abcde']);
  });

  test('reports a broken stream it cannot explain', async () => {
    const h = harness([], { initial: ready('SessionReady'), gets: [ready('Connected')] });
    await h.open();
    h.end({ exitCode: 1, error: 'relay unreachable' });
    await settle();

    expect(h.states.at(-1)).toMatchObject({
      phase: 'ended',
      message: expect.stringMatching(/connection to the instance was lost \(relay unreachable\)/),
    });
  });

  test('reports a clean exit', async () => {
    const h = harness([], { initial: ready('SessionReady') });
    await h.open();
    h.end({ exitCode: 0 });
    await settle();

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
    const h = harness([]);
    await h.open();

    expect(h.states.at(-1)).toMatchObject({
      phase: 'ended',
      message: expect.stringMatching(/too long/),
    });
    expect(h.removed).toEqual(['web-0-abcde']);
    expect(READY_TIMEOUT_MS).toBeGreaterThan(0);
  });

  test('stops watching at the deadline', async () => {
    let release: (() => void) | undefined;
    const hang = new Promise<void>((resolve) => {
      release = resolve;
    });
    const h = harness([]);
    const watchSpy = h.session as unknown as { deps: { watch: unknown } };
    watchSpy.deps.watch = async function* (_n: string, _rv: string, signal: AbortSignal) {
      signal.addEventListener('abort', () => release?.());
      await hang;
      yield* [];
    };
    const opened = h.open();
    await settle();
    h.expire();
    await opened;

    expect(h.states.at(-1)).toMatchObject({
      phase: 'ended',
      message: expect.stringMatching(/too long/),
    });
  });

  test('closing deletes the session, drops the client and ignores late output', async () => {
    const h = harness([], { initial: ready('SessionReady') });
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
    const h = harness([], { initial: ready('SessionReady') });
    await h.open();
    h.session.dispose();

    expect(h.removed).toEqual(['web-0-abcde']);
    expect(h.disposed).toBe(1);
  });

  test('stays connecting until the client reports the connection', async () => {
    const h = harness([], { initial: ready('SessionReady') });
    await h.open();
    h.emit(new Uint8Array([36]));
    expect(h.states.at(-1)).toEqual({ phase: 'connecting' });
  });

  test('ignores input before the connection opens', async () => {
    const h = harness([[modified(ready('NoShell', 'False'))]]);
    await h.open();
    h.session.write('x');
    expect(h.written).toEqual([]);
  });
});
