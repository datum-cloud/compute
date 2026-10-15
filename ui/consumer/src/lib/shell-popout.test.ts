import { describe, expect, test } from 'bun:test';
import { matchPath, matchRoutes } from 'react-router';
import manifest from '../../public/plugin-manifest.json';
import {
  POPUP_BLOCKED_MESSAGE,
  SHELL_WINDOW_FEATURES,
  SHELL_WINDOW_PATH,
  popOutFromTab,
  popOutShell,
  shellWindowHref,
  shellWindowName,
  shellWindowStart,
  createHandoff,
  discardHandoff,
  HANDOFF_TTL_MS,
  handoffTarget,
  type HandoffStorage,
  type ShellWindow,
  type WindowOpener,
} from './shell-popout';

const INSTANCE_PAGE = '/project/:projectId/services/:slug/:workloadName/instances/:instanceName';
const SHELL_HREF = '/project/p1/services/workloads/web/instances/web-abc/shell';

function fakeWindow(href: string, closed = false) {
  const popup = { closed, location: { href }, focused: false } as ShellWindow & {
    focused: boolean;
  };
  popup.focus = () => {
    popup.focused = true;
  };
  return popup;
}

function fakeOpener(popup: ShellWindow | null) {
  const calls: Array<[string, string, string]> = [];
  const opener: WindowOpener = {
    open(url, target, features) {
      calls.push([url, target, features]);
      return popup;
    },
  };
  return { opener, calls };
}

describe('shellWindowHref', () => {
  test('lands on the shell window route of the same instance page', () => {
    const url = new URL(shellWindowHref(SHELL_HREF), 'https://portal.test');
    const match = matchPath(`${INSTANCE_PAGE}/${SHELL_WINDOW_PATH}`, url.pathname);
    expect(match?.params).toMatchObject({
      projectId: 'p1',
      workloadName: 'web',
      instanceName: 'web-abc',
    });
  });

  test('adds no query without a hand-off', () => {
    expect(shellWindowHref(SHELL_HREF)).toBe(
      '/project/p1/services/workloads/web/instances/web-abc/shell/window'
    );
  });

  test('carries only the hand-off nonce, never the command', () => {
    const href = shellWindowHref(`${SHELL_HREF}/`, 'ab'.repeat(16));
    const url = new URL(href, 'https://portal.test');
    expect(url.pathname).toBe('/project/p1/services/workloads/web/instances/web-abc/shell/window');
    expect([...url.searchParams.keys()]).toEqual(['handoff']);
  });
});

function memoryStorage(): HandoffStorage & { keys(): string[] } {
  const items = new Map<string, string>();
  return {
    get length() {
      return items.size;
    },
    key: (i) => [...items.keys()][i] ?? null,
    getItem: (k) => items.get(k) ?? null,
    setItem: (k, v) => void items.set(k, v),
    removeItem: (k) => void items.delete(k),
    keys: () => [...items.keys()],
  };
}

describe('shellWindowStart', () => {
  const containers = ['app', 'sidecar'];
  const target = handoffTarget('p1', 'web-abc');
  let seq = 0;

  function handedOff(storage: HandoffStorage, command: string, now = 1_000) {
    const nonce = (++seq).toString(16).padStart(32, '0');
    createHandoff(storage, { target, container: 'sidecar', command }, now, nonce);
    return new URLSearchParams({ handoff: nonce });
  }

  function start(params: URLSearchParams, storage: HandoffStorage, now = 2_000, t = target) {
    return shellWindowStart(params, containers, { storage, target: t, now, page: new Map() });
  }

  test('a crafted link only fills in the panel and never connects', () => {
    const params = new URLSearchParams({ container: 'sidecar', command: 'rm -rf /' });
    expect(start(params, memoryStorage())).toEqual({
      container: 'sidecar',
      command: 'rm -rf /',
      connect: false,
    });
  });

  test("connects with the container and command from the user's own tab", () => {
    const storage = memoryStorage();
    const params = handedOff(storage, '/bin/bash -l');
    params.set('command', 'rm -rf /');
    expect(start(params, storage)).toEqual({
      container: 'sidecar',
      command: '/bin/bash -l',
      connect: true,
    });
    expect(storage.keys()).toEqual([]);
  });

  test('a replayed hand-off does not connect', () => {
    const storage = memoryStorage();
    const params = handedOff(storage, '/bin/sh');
    expect(start(params, storage).connect).toBe(true);
    expect(start(params, storage).connect).toBe(false);
  });

  test('a render reading the same hand-off twice in one page still connects', () => {
    const storage = memoryStorage();
    const params = handedOff(storage, '/bin/sh');
    const page = new Map();
    const opts = { storage, target, now: 2_000, page };
    expect(shellWindowStart(params, containers, opts).connect).toBe(true);
    expect(shellWindowStart(params, containers, opts).connect).toBe(true);
  });

  test('a nonce this browser never issued does not connect', () => {
    const params = new URLSearchParams({ handoff: 'e'.repeat(32), command: '/bin/sh' });
    expect(start(params, memoryStorage()).connect).toBe(false);
  });

  test('an expired hand-off does not connect and is removed', () => {
    const storage = memoryStorage();
    const params = handedOff(storage, '/bin/sh', 1_000);
    expect(start(params, storage, 1_000 + HANDOFF_TTL_MS).connect).toBe(false);
    expect(storage.keys()).toEqual([]);
  });

  test('a hand-off for another instance does not connect', () => {
    const storage = memoryStorage();
    const params = handedOff(storage, '/bin/sh');
    expect(start(params, storage, 2_000, handoffTarget('p1', 'other')).connect).toBe(false);
  });

  test('writing a hand-off prunes expired ones, and a discarded one is gone', () => {
    const storage = memoryStorage();
    handedOff(storage, '/bin/sh', 0);
    const fresh = handedOff(storage, '/bin/sh', HANDOFF_TTL_MS + 1);
    expect(storage.keys()).toHaveLength(1);
    discardHandoff(storage, fresh.get('handoff') as string);
    expect(storage.keys()).toEqual([]);
  });
});

describe('shellWindowName', () => {
  test('names one window per project and instance', () => {
    expect(shellWindowName('p1', 'web-abc')).toBe('shell-p1-web-abc');
    expect(shellWindowName('p2', 'web-abc')).not.toBe(shellWindowName('p1', 'web-abc'));
  });
});

describe('popOutShell', () => {
  test('opens a popup window and points it at the shell', () => {
    const popup = fakeWindow('about:blank');
    const { opener, calls } = fakeOpener(popup);
    expect(popOutShell(opener, '/shell/window', 'shell-p1-web')).toBe('opened');
    expect(calls).toEqual([['', 'shell-p1-web', SHELL_WINDOW_FEATURES]]);
    expect(popup.location.href).toBe('/shell/window');
  });

  test('focuses an open shell window without reloading it', () => {
    const popup = fakeWindow('https://portal.test/shell/window');
    const { opener } = fakeOpener(popup);
    expect(popOutShell(opener, '/shell/window', 'shell-p1-web')).toBe('focused');
    expect(popup.focused).toBe(true);
    expect(popup.location.href).toBe('https://portal.test/shell/window');
  });

  test('reports a blocked popup', () => {
    expect(popOutShell(fakeOpener(null).opener, '/shell/window', 'shell-p1-web')).toBe('blocked');
    expect(
      popOutShell(fakeOpener(fakeWindow('about:blank', true)).opener, '/shell/window', 'w')
    ).toBe('blocked');
  });
});

describe('popOutFromTab', () => {
  test('closes the tab session once the window opens', () => {
    let closed = 0;
    const message = popOutFromTab({
      opener: fakeOpener(fakeWindow('about:blank')).opener,
      href: '/shell/window',
      name: 'shell-p1-web',
      closeTabSession: () => closed++,
    });
    expect(message).toBeUndefined();
    expect(closed).toBe(1);
  });

  test('keeps the tab session and explains when the popup is blocked', () => {
    let closed = 0;
    const message = popOutFromTab({
      opener: fakeOpener(null).opener,
      href: '/shell/window',
      name: 'shell-p1-web',
      closeTabSession: () => closed++,
    });
    expect(message).toBe(POPUP_BLOCKED_MESSAGE);
    expect(closed).toBe(0);
  });
});

describe('shell window page', () => {
  type Page = {
    type: string;
    properties: { path: string; component: { $codeRef: string }; layout?: string };
    requirements?: { permissions?: Array<{ group: string; resource: string; verb: string }> };
  };
  const pages = (manifest.extensions as Page[]).filter((ext) => ext.type === 'portal.page/project');
  const windowPage = pages.find(
    (page) => page.properties.component.$codeRef === 'InstanceShellWindow'
  );

  function pageAt(pathname: string) {
    const routes = pages.map((page, index) => ({ id: String(index), path: page.properties.path }));
    const matches = matchRoutes(routes, pathname);
    return matches ? pages[Number(matches[matches.length - 1].route.id)] : undefined;
  }

  test('is a bare portal page', () => {
    expect(windowPage?.properties.layout).toBe('bare');
  });

  test('needs the same permission as the instance page', () => {
    expect(windowPage?.requirements?.permissions).toEqual([
      { group: 'compute.datumapis.com', resource: 'instances', verb: 'get' },
    ]);
  });

  test('is exposed by the plugin', () => {
    expect(manifest.exposedModules).toHaveProperty('InstanceShellWindow');
  });

  test('wins over the instance page for the window URL', () => {
    const url = new URL(shellWindowHref(SHELL_HREF, 'sidecar'), 'https://portal.test');
    const mountPath = url.pathname.replace('/project/p1/services/workloads', '');
    expect(pageAt(mountPath)).toBe(windowPage);
  });

  test('leaves the Shell tab on the instance page', () => {
    expect(pageAt('/web/instances/web-abc/shell')?.properties.component.$codeRef).toBe(
      'InstanceDetail'
    );
  });
});
