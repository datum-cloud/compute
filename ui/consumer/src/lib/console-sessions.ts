import { ApiError, getProjectScopedBase } from './api';
import { DEFAULT_SHELL_COMMAND } from './shell-command';

const SESSIONS_PATH =
  '/apis/compute.datumapis.com/v1alpha/namespaces/default/instanceconsolesessions';
const EVENTS_PATH = '/apis/events.k8s.io/v1/namespaces/default/events';
const ALLOWANCE_BUCKETS_PATH =
  '/apis/quota.miloapis.com/v1alpha1/namespaces/milo-system/allowancebuckets?labelSelector=quota.miloapis.com%2Fconsumer-kind%3DProject';

export const SESSION_RESOURCE_TYPE = 'compute.datumapis.com/instanceconsolesessions';

export const SHELL_COMMAND = [DEFAULT_SHELL_COMMAND];

export interface SessionConnection {
  endpointID: string;
  relayURLs: string[];
  target: string;
}

export interface RawConsoleSession {
  metadata?: { name?: string; namespace?: string; uid?: string; resourceVersion?: string };
  status?: {
    connection?: Partial<SessionConnection>;
    exitCode?: number;
    conditions?: Array<{ type?: string; status?: string; reason?: string; message?: string }>;
  };
}

export interface SessionRef {
  name: string;
  namespace: string;
  uid: string;
}

export interface CreatedSession extends SessionRef {
  resourceVersion: string;
  initial: RawConsoleSession;
}

export interface SessionWatchEvent {
  type: 'ADDED' | 'MODIFIED' | 'DELETED' | 'BOOKMARK' | 'ERROR';
  object: RawConsoleSession;
}

export interface SessionEnding {
  reason: string;
  message?: string;
  exitCode?: number;
}

export interface NewSession {
  instanceName: string;
  instanceUid: string;
  containerName: string;
  command: string[];
  clientPublicKey: string;
}

export type SessionProgress =
  | { kind: 'pending' }
  | { kind: 'ready'; connection: SessionConnection }
  | { kind: 'ended'; reason: string; message: string; exitCode?: number };

const REASON_MESSAGES: Record<string, string> = {
  Expired: 'The session reached its time limit.',
  Revoked: 'The session was closed.',
  NotConnected: "The browser didn't reach the shell in time. Try again.",
  AgentShutdown: 'Datum closed the session for maintenance. Connect again to continue.',
  AgentLost: 'Datum lost contact with the session. Connect again to continue.',
  TooManySessions:
    'This instance already has as many shells open as it allows. Close one and try again.',
  NoShell:
    "This container has no shell (sh), so a shell session can't start. Datum runs every command through sh, so no command can run in this container.",
  InstanceNotRunning: "The instance isn't running.",
  InstanceNotFound: 'The instance no longer exists.',
  Invalid: "Datum couldn't open this shell.",
  Unavailable: 'No part of Datum took the session in time. Try again shortly.',
  Disconnected: 'The connection to the session was lost, so Datum stopped the command.',
  ClosedByUser: 'The shell was closed.',
};

export const SESSION_GONE_MESSAGE = "The session ended, but Datum couldn't say why. Try again.";

export const TOO_MANY_SESSIONS_MESSAGE =
  'Too many open sessions in this project. Close another shell and try again.';

export const SESSIONS_NOT_ENABLED_MESSAGE = "Shell sessions aren't enabled for this project.";

export const SESSION_QUOTA_MESSAGE =
  "Shell sessions aren't enabled for this project, or too many are open in it.";

export interface RawAllowanceBucketList {
  items?: Array<{
    spec?: { resourceType?: string };
    status?: { limit?: number };
  }>;
}

export function sessionAllowance(body: RawAllowanceBucketList): number {
  const limit = body.items?.find((b) => b.spec?.resourceType === SESSION_RESOURCE_TYPE)?.status
    ?.limit;
  return typeof limit === 'number' ? limit : 0;
}

export async function fetchSessionAllowance(projectId: string): Promise<number | undefined> {
  try {
    const res = await fetch(`${getProjectScopedBase(projectId)}${ALLOWANCE_BUCKETS_PATH}`, {
      headers: { Accept: 'application/json' },
    });
    if (!res.ok) return undefined;
    return sessionAllowance((await res.json()) as RawAllowanceBucketList);
  } catch {
    return undefined;
  }
}

export function describeEnding(ending: SessionEnding, command: string[] = SHELL_COMMAND): string {
  const { reason, message, exitCode } = ending;
  if (reason === 'Completed' || reason === '') {
    return exitCode ? `The shell exited with code ${exitCode}.` : 'The shell exited.';
  }
  if (reason === 'CommandUnavailable') {
    return command[0]
      ? `${command[0]} isn't installed in this container.`
      : "The command isn't installed in this container.";
  }
  if (reason === 'Invalid') return message?.trim() || REASON_MESSAGES.Invalid;
  return REASON_MESSAGES[reason] ?? (message?.trim() || 'The session ended.');
}

export function sessionEnding(raw: RawConsoleSession): SessionEnding | undefined {
  const ready = raw.status?.conditions?.find((c) => c.type === 'Ready');
  if (ready?.status !== 'False') return undefined;
  return {
    reason: ready.reason ?? '',
    message: ready.message ?? '',
    exitCode: raw.status?.exitCode,
  };
}

export function sessionProgress(raw: RawConsoleSession): SessionProgress {
  const ended = sessionEnding(raw);
  if (ended) return { kind: 'ended', ...ended, message: ended.message ?? '' };
  const ready = raw.status?.conditions?.find((c) => c.type === 'Ready');
  if (!ready || ready.status !== 'True') return { kind: 'pending' };
  const reason = ready.reason ?? '';
  const c = raw.status?.connection;
  if (reason === 'SessionReady' && c?.endpointID && c.target && c.relayURLs?.length) {
    return {
      kind: 'ready',
      connection: { endpointID: c.endpointID, relayURLs: c.relayURLs, target: c.target },
    };
  }
  if (reason === 'Connected') {
    return {
      kind: 'ended',
      reason,
      message: 'Another client is already connected to this session.',
    };
  }
  return { kind: 'pending' };
}

export function isQuotaDenial(message: string): boolean {
  return /reached your quota|insufficient quota/i.test(message);
}

const ADMISSION_PREFIX = /^admission webhook "[^"]*" denied the request: /;

export function createErrorMessage(status: number, raw: string, allowance?: number): string {
  const message = raw.replace(ADMISSION_PREFIX, '');
  if (isQuotaDenial(message)) {
    if (allowance === undefined) return SESSION_QUOTA_MESSAGE;
    return allowance === 0 ? SESSIONS_NOT_ENABLED_MESSAGE : TOO_MANY_SESSIONS_MESSAGE;
  }
  if (/quota/i.test(message)) return message;
  if (status === 403) return "You don't have permission to open a shell in this project.";
  if (status === 404) return 'Shell sessions are not available in this project yet.';
  return message || `The shell could not be opened (${status}).`;
}

async function statusMessage(res: Response): Promise<string> {
  try {
    const body = (await res.json()) as { message?: string };
    return body.message ?? '';
  } catch {
    return '';
  }
}

export async function createSession(projectId: string, input: NewSession): Promise<CreatedSession> {
  const res = await fetch(`${getProjectScopedBase(projectId)}${SESSIONS_PATH}`, {
    method: 'POST',
    headers: { Accept: 'application/json', 'Content-Type': 'application/json' },
    body: JSON.stringify({
      apiVersion: 'compute.datumapis.com/v1alpha',
      kind: 'InstanceConsoleSession',
      metadata: { generateName: `${input.instanceName}-` },
      spec: {
        instanceRef: { name: input.instanceName, uid: input.instanceUid },
        containerName: input.containerName,
        command: input.command,
        stdin: true,
        terminal: true,
        clientPublicKey: input.clientPublicKey,
      },
    }),
  });
  if (!res.ok) {
    const message = await statusMessage(res);
    const allowance = isQuotaDenial(message) ? await fetchSessionAllowance(projectId) : undefined;
    throw new ApiError(res.status, createErrorMessage(res.status, message, allowance));
  }
  const body = (await res.json()) as RawConsoleSession;
  return {
    name: body.metadata?.name ?? '',
    namespace: body.metadata?.namespace ?? 'default',
    uid: body.metadata?.uid ?? '',
    resourceVersion: body.metadata?.resourceVersion ?? '',
    initial: body,
  };
}

export async function* readWatchStream(
  body: ReadableStream<Uint8Array>
): AsyncGenerator<SessionWatchEvent> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffered = '';
  try {
    for (;;) {
      const { value, done } = await reader.read();
      buffered += done ? decoder.decode() : decoder.decode(value, { stream: true });
      const lines = buffered.split('\n');
      buffered = done ? '' : (lines.pop() ?? '');
      for (const line of lines) {
        if (line.trim()) yield JSON.parse(line) as SessionWatchEvent;
      }
      if (done) return;
    }
  } finally {
    reader.releaseLock();
  }
}

export async function* watchSession(
  projectId: string,
  name: string,
  resourceVersion: string,
  signal: AbortSignal
): AsyncGenerator<SessionWatchEvent> {
  const query = new URLSearchParams({ watch: 'true', fieldSelector: `metadata.name=${name}` });
  if (resourceVersion) query.set('resourceVersion', resourceVersion);
  const res = await fetch(`${getProjectScopedBase(projectId)}${SESSIONS_PATH}?${query}`, {
    headers: { Accept: 'application/json' },
    signal,
  });
  if (!res.ok || !res.body) {
    throw new ApiError(res.status, `The session could not be watched (${res.status}).`);
  }
  yield* readWatchStream(res.body);
}

export async function getSession(projectId: string, name: string): Promise<RawConsoleSession> {
  const res = await fetch(`${getProjectScopedBase(projectId)}${SESSIONS_PATH}/${name}`, {
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) throw new ApiError(res.status, `The session could not be read (${res.status}).`);
  return (await res.json()) as RawConsoleSession;
}

export interface RawSessionEvent {
  reason?: string;
  note?: string;
  metadata?: { annotations?: Record<string, string> };
}

interface RawSessionEventList {
  items?: RawSessionEvent[];
}

const REASON_ANNOTATION = 'compute.datumapis.com/reason';
const EXIT_CODE_ANNOTATION = 'compute.datumapis.com/exit-code';

export function sessionEndingFromEvent(event: RawSessionEvent): SessionEnding | undefined {
  const annotations = event.metadata?.annotations ?? {};
  const reason = annotations[REASON_ANNOTATION];
  if (event.reason !== 'SessionEnded' || !reason) return undefined;
  const code = Number.parseInt(annotations[EXIT_CODE_ANNOTATION] ?? '', 10);
  return Number.isNaN(code) ? { reason } : { reason, exitCode: code };
}

async function readJson<T>(url: string): Promise<T | undefined> {
  try {
    const res = await fetch(url, { headers: { Accept: 'application/json' } });
    return res.ok ? ((await res.json()) as T) : undefined;
  } catch {
    return undefined;
  }
}

export async function fetchSessionEnding(
  projectId: string,
  uid: string
): Promise<SessionEnding | undefined> {
  const base = `${getProjectScopedBase(projectId)}${EVENTS_PATH}`;
  const named = await readJson<RawSessionEvent>(`${base}/${uid}.sessionended`);
  const ending = named && sessionEndingFromEvent(named);
  if (ending) return ending;
  const query = new URLSearchParams({ fieldSelector: `regarding.uid=${uid},reason=SessionEnded` });
  const list = await readJson<RawSessionEventList>(`${base}?${query}`);
  for (const event of list?.items ?? []) {
    const found = sessionEndingFromEvent(event);
    if (found) return found;
  }
  return undefined;
}

export async function deleteSession(projectId: string, name: string): Promise<void> {
  await fetch(`${getProjectScopedBase(projectId)}${SESSIONS_PATH}/${name}`, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
    keepalive: true,
  }).catch(() => undefined);
}
