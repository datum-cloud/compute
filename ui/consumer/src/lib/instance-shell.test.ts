import { describe, expect, test } from 'bun:test';
import {
  canShowShell,
  initialContainer,
  resolveRuntimeClass,
  shellStatusBadge,
  toRuntimeClasses,
  type RuntimeClassSummary,
} from './instance-shell';

const classes: RuntimeClassSummary[] = toRuntimeClasses({
  items: [
    {
      metadata: { name: 'general-purpose' },
      spec: { default: true, capabilities: { features: ['sandboxRuntime', 'exec'] } },
    },
    {
      metadata: { name: 'unikernel' },
      spec: { capabilities: { features: ['sandboxRuntime'] } },
    },
  ],
});

describe('resolveRuntimeClass', () => {
  test('uses the class the instance names', () => {
    expect(resolveRuntimeClass('unikernel', classes)?.name).toBe('unikernel');
  });

  test('falls back to the default class when the instance names none', () => {
    expect(resolveRuntimeClass(undefined, classes)?.name).toBe('general-purpose');
  });

  test('is undefined for a class the catalog does not publish', () => {
    expect(resolveRuntimeClass('missing', classes)).toBeUndefined();
  });
});

describe('canShowShell', () => {
  const shown = {
    clientBundled: true,
    canCreateSessions: true,
    sessionAllowance: 5 as number | undefined,
    runtimeClass: resolveRuntimeClass('general-purpose', classes),
    containers: ['app'],
  };

  test('shows with permission, an allowance, an exec class and a container', () => {
    expect(canShowShell(shown)).toBe(true);
  });

  test('hides without permission to create sessions', () => {
    expect(canShowShell({ ...shown, canCreateSessions: false })).toBe(false);
  });

  test('hides when the project has no session allowance', () => {
    expect(canShowShell({ ...shown, sessionAllowance: 0 })).toBe(false);
  });

  test('shows when the allowance cannot be read', () => {
    expect(canShowShell({ ...shown, sessionAllowance: undefined })).toBe(true);
  });

  test('hides when the runtime class lacks exec', () => {
    expect(
      canShowShell({ ...shown, runtimeClass: resolveRuntimeClass('unikernel', classes) })
    ).toBe(false);
  });

  test('hides when the runtime class is unknown', () => {
    expect(canShowShell({ ...shown, runtimeClass: undefined })).toBe(false);
  });

  test('hides when the plugin was built without the shell client', () => {
    expect(canShowShell({ ...shown, clientBundled: false })).toBe(false);
  });

  test('hides for instances without containers', () => {
    expect(canShowShell({ ...shown, containers: [] })).toBe(false);
  });
});

describe('initialContainer', () => {
  test('selects the only container', () => {
    expect(initialContainer(['app'])).toBe('app');
  });

  test('selects the first of several, which the picker still shows', () => {
    expect(initialContainer(['app', 'sidecar'])).toBe('app');
  });

  test('selects nothing when there are none', () => {
    expect(initialContainer([])).toBeUndefined();
  });
});

describe('shellStatusBadge', () => {
  test('names each phase and picks its colour', () => {
    expect(shellStatusBadge({ phase: 'connected' })).toEqual({
      label: 'Connected',
      type: 'success',
    });
    expect(shellStatusBadge({ phase: 'ended' })).toEqual({ label: 'Ended', type: 'muted' });
  });

  test('treats every phase before the shell as opening', () => {
    for (const phase of ['starting', 'waiting', 'connecting']) {
      expect(shellStatusBadge({ phase })).toEqual({ label: 'Opening', type: 'warning' });
    }
  });
});
