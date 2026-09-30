import { describe, expect, test } from 'bun:test';
import { matchPath } from 'react-router';
import {
  POPUP_BLOCKED_MESSAGE,
  SHELL_WINDOW_FEATURES,
  SHELL_WINDOW_PATH,
  popOutFromTab,
  popOutShell,
  shellWindowHref,
  shellWindowName,
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
  test('lands on the pop-out route of the same instance page', () => {
    const url = new URL(shellWindowHref(SHELL_HREF), 'https://portal.test');
    const match = matchPath(`${INSTANCE_PAGE}/${SHELL_WINDOW_PATH}`, url.pathname);
    expect(match?.params).toMatchObject({
      projectId: 'p1',
      workloadName: 'web',
      instanceName: 'web-abc',
    });
  });

  test('starts the portal sidebar collapsed', () => {
    const url = new URL(shellWindowHref(SHELL_HREF), 'https://portal.test');
    expect(url.searchParams.get('sidebar')).toBe('false');
  });

  test('carries the chosen container', () => {
    const url = new URL(shellWindowHref(`${SHELL_HREF}/`, 'sidecar'), 'https://portal.test');
    expect(url.pathname).toBe('/project/p1/services/workloads/web/instances/web-abc/shell/window');
    expect(url.searchParams.get('container')).toBe('sidecar');
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

  test('focuses an existing pop-out without reloading it', () => {
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
  test('closes the tab session once the pop-out opens', () => {
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
