import { describe, expect, test } from 'bun:test';
import {
  createErrorMessage,
  reasonMessage,
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

  test('surfaces the platform message for a terminal reason', () => {
    expect(sessionProgress(withReady('False', 'NoShell', {}, 'The image has no /bin/sh.'))).toEqual(
      { kind: 'ended', reason: 'NoShell', message: 'The image has no /bin/sh.' }
    );
  });

  test('explains a terminal reason that carries no message', () => {
    const progress = sessionProgress(withReady('False', 'InstanceNotRunning'));
    expect(progress).toMatchObject({ kind: 'ended', message: 'The instance is not running.' });
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

describe('reasonMessage', () => {
  test('explains a session no cell took in time', () => {
    expect(reasonMessage('Unavailable')).toMatch(/aren't available/);
  });

  test('explains a session whose client went away', () => {
    expect(reasonMessage('Disconnected')).toMatch(/connection to it was lost/);
    expect(reasonMessage('ClosedByUser')).toBe('The shell was closed.');
  });

  test('prefers the platform message', () => {
    expect(reasonMessage('Unavailable', 'No cell is serving this instance.')).toBe(
      'No cell is serving this instance.'
    );
  });
});

describe('sessionAllowance', () => {
  test('reads the limit of the session bucket', () => {
    expect(
      sessionAllowance({
        items: [
          { spec: { resourceType: 'compute.datumapis.com/instances' }, status: { limit: 50 } },
          { spec: { resourceType: SESSION_RESOURCE_TYPE }, status: { limit: 3 } },
        ],
      })
    ).toBe(3);
  });

  test('is zero without a session bucket', () => {
    expect(sessionAllowance({ items: [] })).toBe(0);
    expect(sessionAllowance({})).toBe(0);
  });

  test('is zero before the bucket reports a limit', () => {
    expect(sessionAllowance({ items: [{ spec: { resourceType: SESSION_RESOURCE_TYPE } }] })).toBe(
      0
    );
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
