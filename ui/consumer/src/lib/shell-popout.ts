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

export function shellWindowHref(shellHref: string, handoff?: string): string {
  const base = shellHref.replace(/\/$/, '').replace(/\/shell$/, '');
  const query = handoff ? `?${new URLSearchParams({ handoff })}` : '';
  return `${base}/${SHELL_WINDOW_PATH}${query}`;
}

export const HANDOFF_TTL_MS = 30_000;

const HANDOFF_PREFIX = 'compute-shell-handoff:';
const NONCE = /^[0-9a-f]{32}$/;

export interface HandoffStorage {
  readonly length: number;
  key(index: number): string | null;
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

interface HandoffRecord {
  target: string;
  container: string;
  command: string;
  expiresAt: number;
}

export function handoffStorage(): HandoffStorage | undefined {
  try {
    return window.localStorage;
  } catch {
    return undefined;
  }
}

export function handoffTarget(projectId: string, instanceName: string): string {
  return `${projectId}/${instanceName}`;
}

function randomNonce(): string {
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return Array.from(bytes, (b) => b.toString(16).padStart(2, '0')).join('');
}

function pruneExpired(storage: HandoffStorage, now: number) {
  const keys: string[] = [];
  for (let i = 0; i < storage.length; i++) {
    const key = storage.key(i);
    if (key?.startsWith(HANDOFF_PREFIX)) keys.push(key);
  }
  for (const key of keys) {
    try {
      const record = JSON.parse(storage.getItem(key) ?? '') as HandoffRecord;
      if (!(record.expiresAt > now)) storage.removeItem(key);
    } catch {
      storage.removeItem(key);
    }
  }
}

// createHandoff records what the user chose in their own tab, so the window
// it opens can connect without asking again. Only this browser's storage
// holds the record, so a link cannot carry one, and the window uses a record
// once.
export function createHandoff(
  storage: HandoffStorage,
  start: { target: string; container: string; command: string },
  now: number,
  nonce: string = randomNonce()
): string {
  pruneExpired(storage, now);
  const record: HandoffRecord = { ...start, expiresAt: now + HANDOFF_TTL_MS };
  storage.setItem(HANDOFF_PREFIX + nonce, JSON.stringify(record));
  return nonce;
}

export function discardHandoff(storage: HandoffStorage, nonce: string) {
  storage.removeItem(HANDOFF_PREFIX + nonce);
}

// A render can read the hand-off more than once, so the first read in this
// page is remembered. A reload starts a new page and finds the record gone.
const pageReads = new Map<string, Required<ShellStart> | undefined>();

export function consumeHandoff(
  storage: HandoffStorage,
  nonce: string,
  target: string,
  now: number,
  consumed: Map<string, Required<ShellStart> | undefined> = pageReads
): Required<ShellStart> | undefined {
  if (!NONCE.test(nonce)) return undefined;
  if (consumed.has(nonce)) return consumed.get(nonce);
  let start: Required<ShellStart> | undefined;
  try {
    const raw = storage.getItem(HANDOFF_PREFIX + nonce);
    storage.removeItem(HANDOFF_PREFIX + nonce);
    const record = raw ? (JSON.parse(raw) as HandoffRecord) : undefined;
    if (record && record.target === target && record.expiresAt > now) {
      start = { container: record.container, command: record.command };
    }
  } catch {
    start = undefined;
  }
  consumed.set(nonce, start);
  return start;
}

export interface WindowStart {
  container: string | undefined;
  command: string;
  connect: boolean;
}

// shellWindowStart decides what a shell window starts with. A link's
// container and command only fill in the start panel, so a crafted link can
// never run a command without the user choosing to connect. The window
// connects at once only for a valid hand-off from the user's own tab.
export function shellWindowStart(
  params: URLSearchParams,
  containers: string[],
  handoff: {
    storage: HandoffStorage | undefined;
    target: string;
    now: number;
    page?: Map<string, Required<ShellStart> | undefined>;
  }
): WindowStart {
  const nonce = params.get('handoff');
  const handed =
    nonce && handoff.storage
      ? consumeHandoff(handoff.storage, nonce, handoff.target, handoff.now, handoff.page)
      : undefined;
  if (handed && containers.includes(handed.container) && parseCommand(handed.command).ok) {
    return { container: handed.container, command: handed.command, connect: true };
  }
  const requested = params.get('container') ?? '';
  return {
    container: containers.includes(requested) ? requested : containers[0],
    command: params.get('command') ?? DEFAULT_SHELL_COMMAND,
    connect: false,
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
