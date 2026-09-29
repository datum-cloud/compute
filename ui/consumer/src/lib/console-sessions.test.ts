import { describe, expect, test } from 'bun:test';
import {
  createErrorMessage,
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

describe('createErrorMessage', () => {
  test('maps a quota denial to too many open sessions', () => {
    expect(
      createErrorMessage(403, 'instanceconsolesessions "x" is forbidden: Insufficient quota')
    ).toBe(TOO_MANY_SESSIONS_MESSAGE);
  });

  test('maps other forbidden responses to a permission message', () => {
    expect(createErrorMessage(403, 'cannot create resource')).toMatch(/permission/);
  });

  test('passes other API messages through', () => {
    expect(createErrorMessage(400, 'shell sessions are not enabled')).toBe(
      'shell sessions are not enabled'
    );
  });
});
