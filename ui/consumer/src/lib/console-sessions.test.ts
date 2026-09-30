import { afterEach, describe, expect, test } from 'bun:test';
import {
  createErrorMessage,
  createSession,
  describeEnding,
  fetchSessionEnding,
  readWatchStream,
  sessionEndingFromEvent,
  SESSION_QUOTA_MESSAGE,
  SESSION_RESOURCE_TYPE,
  sessionAllowance,
  SESSIONS_NOT_ENABLED_MESSAGE,
  sessionProgress,
  TOO_MANY_SESSIONS_MESSAGE,
  type RawConsoleSession,
} from './console-sessions';

function withReady(
  status: string,
  reason: string,
  extra: Partial<NonNullable<RawConsoleSession['status']>> = {},
  message?: string
): RawConsoleSession {
  return { status: { ...extra, conditions: [{ type: 'Ready', status, reason, message }] } };
}

const connection = {
  endpointID: 'ab'.repeat(32),
  relayURLs: ['https://relay.example'],
  target: 'agent.cell:8443',
};

describe('sessionProgress', () => {
  test('is pending without a Ready condition', () => {
    expect(sessionProgress({})).toEqual({ kind: 'pending' });
  });

  test('is pending while Ready is Unknown', () => {
    expect(sessionProgress(withReady('Unknown', 'Pending'))).toEqual({ kind: 'pending' });
  });

  test('is ready once SessionReady publishes a connection', () => {
    expect(sessionProgress(withReady('True', 'SessionReady', { connection }))).toEqual({
      kind: 'ready',
      connection,
    });
  });

  test('waits for a complete connection', () => {
    expect(
      sessionProgress(withReady('True', 'SessionReady', { connection: { endpointID: 'x' } }))
    ).toEqual({ kind: 'pending' });
  });

  test('carries the terminal reason and the platform message', () => {
    expect(sessionProgress(withReady('False', 'NoShell', {}, 'The image has no /bin/sh.'))).toEqual(
      {
        kind: 'ended',
        reason: 'NoShell',
        message: 'The image has no /bin/sh.',
        exitCode: undefined,
      }
    );
  });

  test('carries the exit code of a completed session', () => {
    expect(sessionProgress(withReady('False', 'Completed', { exitCode: 3 }))).toMatchObject({
      kind: 'ended',
      exitCode: 3,
    });
  });

  test('ends a session another client already consumed', () => {
    expect(sessionProgress(withReady('True', 'Connected', { connection }))).toMatchObject({
      kind: 'ended',
      reason: 'Connected',
    });
  });
});

describe('describeEnding', () => {
  test('says plainly that a container without sh can run nothing', () => {
    const message = describeEnding({
      reason: 'NoShell',
      message: 'The container has no shell (sh).',
    });
    expect(message).toStartWith(
      "This container has no shell (sh), so a shell session can't start."
    );
    expect(message).toMatch(/no command can run/);
  });

  test('names the command that is not installed', () => {
    expect(describeEnding({ reason: 'CommandUnavailable' }, ['/bin/bash', '-l'])).toBe(
      "/bin/bash isn't installed in this container."
    );
  });

  test('reports the exit code of a command that exited', () => {
    expect(describeEnding({ reason: 'Completed', exitCode: 0 })).toBe('The shell exited.');
    expect(describeEnding({ reason: 'Completed', exitCode: 3 })).toBe(
      'The shell exited with code 3.'
    );
  });

  test('reuses the CLI wording for platform endings', () => {
    expect(describeEnding({ reason: 'Unavailable' })).toBe(
      'No part of Datum took the session in time. Try again shortly.'
    );
    expect(describeEnding({ reason: 'Disconnected' })).toBe(
      'The connection to the session was lost, so Datum stopped the command.'
    );
  });

  test('tells a deliberate close from a lost connection', () => {
    expect(describeEnding({ reason: 'ClosedByUser' })).toBe('The shell was closed.');
  });

  test('explains every terminal reason', () => {
    for (const reason of [
      'Expired',
      'Revoked',
      'NotConnected',
      'AgentShutdown',
      'AgentLost',
      'TooManySessions',
      'NoShell',
      'CommandUnavailable',
      'InstanceNotRunning',
      'InstanceNotFound',
      'Invalid',
      'Unavailable',
      'Disconnected',
      'ClosedByUser',
    ]) {
      expect(describeEnding({ reason })).not.toBe('The session ended.');
    }
  });

  test('prefers the platform message for a request it could not serve', () => {
    expect(describeEnding({ reason: 'Invalid', message: 'The command is too long.' })).toBe(
      'The command is too long.'
    );
  });

  test('falls back to the platform message for a reason it does not know', () => {
    expect(describeEnding({ reason: 'SomethingNew', message: 'Details.' })).toBe('Details.');
  });
});

function streamOf(...chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk));
      controller.close();
    },
  });
}

describe('readWatchStream', () => {
  test('parses watch events split across chunks', async () => {
    const events = [];
    for await (const event of readWatchStream(
      streamOf(
        '{"type":"MODIFIED","object":{"metadata":{"resourceVersion":"2"}}}\n{"type":"DEL',
        'ETED","object":{"metadata":{"resourceVersion":"3"}}}\n'
      )
    )) {
      events.push(event);
    }
    expect(events.map((e) => [e.type, e.object.metadata?.resourceVersion])).toEqual([
      ['MODIFIED', '2'],
      ['DELETED', '3'],
    ]);
  });

  test('reads a final event without a trailing newline', async () => {
    const events = [];
    for await (const event of readWatchStream(streamOf('{"type":"ADDED","object":{}}'))) {
      events.push(event.type);
    }
    expect(events).toEqual(['ADDED']);
  });
});

function endedEvent(annotations: Record<string, string>) {
  return { reason: 'SessionEnded', metadata: { annotations } };
}

describe('sessionEndingFromEvent', () => {
  test('reads the reason and exit code the controller records', () => {
    expect(
      sessionEndingFromEvent(
        endedEvent({
          'compute.datumapis.com/reason': 'Completed',
          'compute.datumapis.com/exit-code': '2',
        })
      )
    ).toEqual({ reason: 'Completed', exitCode: 2 });
  });

  test('ignores events other than the session ending', () => {
    expect(
      sessionEndingFromEvent({ reason: 'SessionStarted', metadata: { annotations: {} } })
    ).toBeUndefined();
  });
});

describe('fetchSessionEnding', () => {
  const realFetch = globalThis.fetch;
  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  function serve(routes: Record<string, unknown>) {
    const seen: string[] = [];
    globalThis.fetch = (async (input: string | URL | Request) => {
      const url = decodeURIComponent(String(input));
      seen.push(url);
      const key = Object.keys(routes).find((k) => url.endsWith(k));
      return key
        ? new Response(JSON.stringify(routes[key]), { status: 200 })
        : new Response('{}', { status: 404 });
    }) as typeof fetch;
    return seen;
  }

  test("reads the session's SessionEnded event by name", async () => {
    const seen = serve({
      '/events/session-uid.sessionended': endedEvent({ 'compute.datumapis.com/reason': 'NoShell' }),
    });
    expect(await fetchSessionEnding('p1', 'session-uid')).toEqual({ reason: 'NoShell' });
    expect(seen).toHaveLength(1);
    expect(seen[0]).toContain('/apis/events.k8s.io/v1/namespaces/default/events/');
  });

  test('finds the event by the session it regards when the name differs', async () => {
    const seen = serve({
      'fieldSelector=regarding.uid=session-uid,reason=SessionEnded': {
        items: [endedEvent({ 'compute.datumapis.com/reason': 'Expired' })],
      },
    });
    expect(await fetchSessionEnding('p1', 'session-uid')).toEqual({ reason: 'Expired' });
    expect(seen).toHaveLength(2);
  });

  test('returns nothing when no ending was recorded', async () => {
    serve({});
    expect(await fetchSessionEnding('p1', 'session-uid')).toBeUndefined();
  });
});

describe('createErrorMessage', () => {
  const denial =
    "You've reached your quota for this resource type (Insufficient quota resources.). Delete unused resources to free up capacity, or contact support to request a higher limit.";

  test('maps a denial with no allowance to sessions not enabled', () => {
    expect(createErrorMessage(403, denial, 0)).toBe(SESSIONS_NOT_ENABLED_MESSAGE);
  });

  test('maps a denial with an allowance to too many open sessions', () => {
    expect(createErrorMessage(403, denial, 10)).toBe(TOO_MANY_SESSIONS_MESSAGE);
  });

  test('covers both causes when the allowance cannot be read', () => {
    expect(
      createErrorMessage(403, 'instanceconsolesessions "x" is forbidden: Insufficient quota')
    ).toBe(SESSION_QUOTA_MESSAGE);
  });

  test('passes a quota check that is not a denial through', () => {
    const timeout =
      'Your request took too long to be checked against your quota. Please try again in a moment.';
    expect(createErrorMessage(403, timeout)).toBe(timeout);
  });

  test('maps other forbidden responses to a permission message', () => {
    expect(createErrorMessage(403, 'cannot create resource')).toMatch(/permission/);
  });

  test('shows an admission refusal without the webhook preamble', () => {
    expect(
      createErrorMessage(
        422,
        'admission webhook "minstanceconsolesession.kb.io" denied the request: Can\'t open a shell session in instance "web-0": the "unikernel" runtime class does not support shell sessions'
      )
    ).toBe(
      'Can\'t open a shell session in instance "web-0": the "unikernel" runtime class does not support shell sessions'
    );
  });

  test('passes other API messages through', () => {
    expect(createErrorMessage(400, 'shell sessions are not enabled')).toBe(
      'shell sessions are not enabled'
    );
  });
});

describe('createSession', () => {
  const realFetch = globalThis.fetch;
  afterEach(() => {
    globalThis.fetch = realFetch;
  });

  test('sends the chosen command as the session argv', async () => {
    let body: { spec?: { command?: string[]; containerName?: string } } = {};
    globalThis.fetch = (async (_input: string | URL | Request, init?: RequestInit) => {
      body = JSON.parse(String(init?.body));
      return new Response(
        JSON.stringify({ metadata: { name: 'web-0-x', uid: 'u', resourceVersion: '4' } }),
        { status: 201 }
      );
    }) as typeof fetch;

    const created = await createSession('p1', {
      instanceName: 'web-0',
      instanceUid: 'iu',
      containerName: 'app',
      command: ['/bin/bash', '-l'],
      clientPublicKey: 'cd'.repeat(32),
    });

    expect(body.spec).toMatchObject({ containerName: 'app', command: ['/bin/bash', '-l'] });
    expect(created).toMatchObject({ name: 'web-0-x', uid: 'u', resourceVersion: '4' });
  });
});
