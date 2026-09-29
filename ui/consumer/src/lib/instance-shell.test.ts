import { describe, expect, test } from 'bun:test';
import {
  canShowShell,
  initialContainer,
  resolveRuntimeClass,
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
  const execClass = resolveRuntimeClass('general-purpose', classes);

  test('shows with permission, an exec class and a container', () => {
    expect(
      canShowShell({
        clientBundled: true,
        canCreateSessions: true,
        runtimeClass: execClass,
        containers: ['app'],
      })
    ).toBe(true);
  });

  test('hides without permission to create sessions', () => {
    expect(
      canShowShell({
        clientBundled: true,
        canCreateSessions: false,
        runtimeClass: execClass,
        containers: ['app'],
      })
    ).toBe(false);
  });

  test('hides when the runtime class lacks exec', () => {
    expect(
      canShowShell({
        clientBundled: true,
        canCreateSessions: true,
        runtimeClass: resolveRuntimeClass('unikernel', classes),
        containers: ['app'],
      })
    ).toBe(false);
  });

  test('hides when the runtime class is unknown', () => {
    expect(
      canShowShell({
        clientBundled: true,
        canCreateSessions: true,
        runtimeClass: undefined,
        containers: ['app'],
      })
    ).toBe(false);
  });

  test('hides when the plugin was built without the shell client', () => {
    expect(
      canShowShell({
        clientBundled: false,
        canCreateSessions: true,
        runtimeClass: execClass,
        containers: ['app'],
      })
    ).toBe(false);
  });

  test('hides for instances without containers', () => {
    expect(
      canShowShell({
        clientBundled: true,
        canCreateSessions: true,
        runtimeClass: execClass,
        containers: [],
      })
    ).toBe(false);
  });
});

describe('initialContainer', () => {
  test('selects the only container', () => {
    expect(initialContainer(['app'])).toBe('app');
  });

  test('leaves the choice to the user when there are several', () => {
    expect(initialContainer(['app', 'sidecar'])).toBeUndefined();
  });

  test('selects nothing when there are none', () => {
    expect(initialContainer([])).toBeUndefined();
  });
});
