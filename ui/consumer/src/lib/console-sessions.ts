import { ApiError, getProjectScopedBase } from './api';

const SESSIONS_PATH =
  '/apis/compute.datumapis.com/v1alpha/namespaces/default/instanceconsolesessions';

export const SHELL_COMMAND = ['sh'];

export interface SessionConnection {
  endpointID: string;
  relayURLs: string[];
  target: string;
}

export interface RawConsoleSession {
  metadata?: { name?: string; namespace?: string; uid?: string };
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

export interface NewSession {
  instanceName: string;
  instanceUid: string;
  containerName: string;
  clientPublicKey: string;
}

export type SessionProgress =
  | { kind: 'pending' }
  | { kind: 'ready'; connection: SessionConnection }
  | { kind: 'ended'; reason: string; message: string; exitCode?: number };

const REASON_MESSAGES: Record<string, string> = {
  Completed: 'The shell exited.',
  Expired: 'The session reached its time limit.',
  Revoked: 'The session was closed.',
  NotConnected: 'The shell was not reached in time. Open a new shell to try again.',
  AgentShutdown: 'The platform closed the session for maintenance. Open a new shell to continue.',
  AgentLost: 'The platform lost contact with the session. Open a new shell to continue.',
  TooManySessions:
    'This instance already has the most shells it can hold open. Close one and try again.',
  NoShell: 'This container has no shell to open.',
  CommandUnavailable: 'This container cannot run a shell.',
  InstanceNotRunning: 'The instance is not running.',
  InstanceNotFound: 'The instance no longer exists.',
  Invalid: 'The platform could not open this shell.',
};

export const TOO_MANY_SESSIONS_MESSAGE =
  'There are too many open sessions in this project. Close another shell and try again.';

export function reasonMessage(reason: string, message?: string): string {
  return message?.trim() || REASON_MESSAGES[reason] || 'The session ended.';
}

export function sessionProgress(raw: RawConsoleSession): SessionProgress {
  const ready = raw.status?.conditions?.find((c) => c.type === 'Ready');
  if (!ready || ready.status === 'Unknown' || !ready.status) return { kind: 'pending' };
  const reason = ready.reason ?? '';
  if (ready.status === 'False') {
    return {
      kind: 'ended',
      reason,
      message: reasonMessage(reason, ready.message),
      exitCode: raw.status?.exitCode,
    };
  }
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

export function createErrorMessage(status: number, message: string): string {
  if (/quota/i.test(message)) return TOO_MANY_SESSIONS_MESSAGE;
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

export async function createSession(projectId: string, input: NewSession): Promise<SessionRef> {
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
        command: SHELL_COMMAND,
        stdin: true,
        terminal: true,
        clientPublicKey: input.clientPublicKey,
      },
    }),
  });
  if (!res.ok) {
    throw new ApiError(res.status, createErrorMessage(res.status, await statusMessage(res)));
  }
  const body = (await res.json()) as RawConsoleSession;
  return {
    name: body.metadata?.name ?? '',
    namespace: body.metadata?.namespace ?? 'default',
    uid: body.metadata?.uid ?? '',
  };
}

export async function getSession(projectId: string, name: string): Promise<RawConsoleSession> {
  const res = await fetch(`${getProjectScopedBase(projectId)}${SESSIONS_PATH}/${name}`, {
    headers: { Accept: 'application/json' },
  });
  if (!res.ok) throw new ApiError(res.status, `The session could not be read (${res.status}).`);
  return (await res.json()) as RawConsoleSession;
}

export async function deleteSession(projectId: string, name: string): Promise<void> {
  await fetch(`${getProjectScopedBase(projectId)}${SESSIONS_PATH}/${name}`, {
    method: 'DELETE',
    headers: { Accept: 'application/json' },
    keepalive: true,
  }).catch(() => undefined);
}
