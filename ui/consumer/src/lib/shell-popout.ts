import { DEFAULT_SHELL_COMMAND, parseCommand } from './shell-command';

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

export interface ShellStart {
  container?: string;
  command?: string;
}

export function shellWindowHref(shellHref: string, start: ShellStart = {}): string {
  const base = shellHref.replace(/\/$/, '').replace(/\/shell$/, '');
  const params = new URLSearchParams();
  if (start.container) params.set('container', start.container);
  if (start.command?.trim()) params.set('command', start.command);
  const query = params.size ? `?${params}` : '';
  return `${base}/${SHELL_WINDOW_PATH}${query}`;
}

export interface WindowStart {
  container: string | undefined;
  command: string;
  connect: boolean;
}

// shellWindowStart reads the container and command a shell window was opened
// with. The window connects at once only when both are usable; otherwise it
// shows them in its start panel to correct.
export function shellWindowStart(params: URLSearchParams, containers: string[]): WindowStart {
  const requested = params.get('container') ?? '';
  const container = containers.includes(requested) ? requested : containers[0];
  const command = params.get('command') ?? DEFAULT_SHELL_COMMAND;
  const containerOK = !requested || requested === container;
  return {
    container,
    command,
    connect: !!container && containerOK && parseCommand(command).ok,
  };
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
