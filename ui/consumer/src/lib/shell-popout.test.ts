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

  test('adds no query without a container', () => {
    expect(shellWindowHref(SHELL_HREF)).toBe(
      '/project/p1/services/workloads/web/instances/web-abc/shell/window'
    );
  });

  test('carries the chosen container and command', () => {
    const command = `sh -c 'echo "a&b=c" #1'`;
    const href = shellWindowHref(`${SHELL_HREF}/`, { container: 'sidecar', command });
    const url = new URL(href, 'https://portal.test');
    expect(url.pathname).toBe('/project/p1/services/workloads/web/instances/web-abc/shell/window');
    expect(url.searchParams.get('container')).toBe('sidecar');
    expect(url.searchParams.get('command')).toBe(command);
    expect(href).not.toContain('&b=c');
    expect(href).not.toContain('#');
  });

  test('leaves out a blank command', () => {
    expect(shellWindowHref(SHELL_HREF, { container: 'app', command: '  ' })).toBe(
      '/project/p1/services/workloads/web/instances/web-abc/shell/window?container=app'
    );
  });
});

describe('shellWindowStart', () => {
  const containers = ['app', 'sidecar'];

  test('connects with the container and command the window was opened with', () => {
    const href = shellWindowHref(SHELL_HREF, { container: 'sidecar', command: '/bin/bash -l' });
    const params = new URL(href, 'https://portal.test').searchParams;
    expect(shellWindowStart(params, containers)).toEqual({
      container: 'sidecar',
      command: '/bin/bash -l',
      connect: true,
    });
  });

  test('defaults to the first container and /bin/sh', () => {
    expect(shellWindowStart(new URLSearchParams(), containers)).toEqual({
      container: 'app',
      command: '/bin/sh',
      connect: true,
    });
  });

  test('waits for the user when the container is not one the instance runs', () => {
    expect(shellWindowStart(new URLSearchParams({ container: 'gone' }), containers)).toEqual({
      container: 'app',
      command: '/bin/sh',
      connect: false,
    });
  });

  test('waits for the user when the command does not parse', () => {
    const start = shellWindowStart(new URLSearchParams({ command: `sh -c 'echo` }), containers);
    expect(start).toMatchObject({ command: `sh -c 'echo`, connect: false });
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
