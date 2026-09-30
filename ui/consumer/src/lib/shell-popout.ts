export const SHELL_WINDOW_PATH = 'shell/window';

export const SHELL_WINDOW_FEATURES = 'popup,width=960,height=640';

export const POPUP_BLOCKED_MESSAGE =
  'Your browser blocked the shell window. Allow pop-ups for this site and try again.';

export type PopOutResult = 'opened' | 'focused' | 'blocked';

export interface ShellWindow {
  closed: boolean;
  location: { href: string };
  focus(): void;
}

export interface WindowOpener {
  open(url: string, target: string, features: string): ShellWindow | null;
}

export function shellWindowName(projectId: string, instanceName: string): string {
  return `shell-${projectId}-${instanceName}`;
}

export function shellWindowHref(shellHref: string, container?: string): string {
  const base = shellHref.replace(/\/$/, '').replace(/\/shell$/, '');
  const query = container ? `?${new URLSearchParams({ container })}` : '';
  return `${base}/${SHELL_WINDOW_PATH}${query}`;
}

function isBlank(popup: ShellWindow): boolean {
  try {
    return popup.location.href === 'about:blank';
  } catch {
    return false;
  }
}

export function popOutShell(opener: WindowOpener, href: string, name: string): PopOutResult {
  const popup = opener.open('', name, SHELL_WINDOW_FEATURES);
  if (!popup || popup.closed) return 'blocked';
  if (isBlank(popup)) {
    popup.location.href = href;
    return 'opened';
  }
  popup.focus();
  return 'focused';
}

export function popOutFromTab({
  opener,
  href,
  name,
  closeTabSession,
}: {
  opener: WindowOpener;
  href: string;
  name: string;
  closeTabSession(): void;
}): string | undefined {
  if (popOutShell(opener, href, name) === 'blocked') return POPUP_BLOCKED_MESSAGE;
  closeTabSession();
  return undefined;
}
