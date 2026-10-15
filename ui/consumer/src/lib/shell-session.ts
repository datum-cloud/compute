import { ApiError } from './api';
import type { ShellConnector } from './console-client/connector';
import type { ConsoleEnding, TerminalSize } from './console-client/wasm-adapter';
import {
  describeEnding,
  SESSION_GONE_MESSAGE,
  SHELL_COMMAND,
  sessionEnding,
  sessionProgress,
  type CreatedSession,
  type NewSession,
  type RawConsoleSession,
  type SessionEnding,
  type SessionProgress,
  type SessionRef,
  type SessionWatchEvent,
} from './console-sessions';

export type ShellState =
  | { phase: 'idle' }
  | { phase: 'starting' }
  | { phase: 'waiting' }
  | { phase: 'connecting' }
  | { phase: 'connected' }
  | { phase: 'ended'; message: string };

export interface ShellSessionDeps {
  createConnector(): ShellConnector;
  create(input: NewSession): Promise<CreatedSession>;
  watch(
    name: string,
    resourceVersion: string,
    signal: AbortSignal
  ): AsyncIterable<SessionWatchEvent>;
  get(name: string): Promise<RawConsoleSession>;
  ending(uid: string): Promise<SessionEnding | undefined>;
  remove(name: string): Promise<void>;
  wait(ms: number, signal?: AbortSignal): Promise<void>;
  now(): number;
}

export interface ShellSessionOptions {
  instanceName: string;
  instanceUid: string;
  onState(state: ShellState): void;
  onOutput(bytes: Uint8Array): void;
}

export const POLL_INTERVAL_MS = 500;
export const READY_TIMEOUT_MS = 60_000;

export function endingMessage(ending: ConsoleEnding, command: string[] = SHELL_COMMAND): string {
  if (ending.reason) return describeEnding(ending, command);
  if (ending.error === 'closed') return 'The shell was closed.';
  if (ending.error) {
    return `The connection to the instance was lost (${ending.error}). Open a new shell to try again.`;
  }
  return describeEnding({ reason: 'Completed', exitCode: ending.exitCode }, command);
}

function errorMessage(err: unknown): string {
  return err instanceof Error && err.message ? err.message : 'The shell could not be opened.';
}

function isGone(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404;
}

type ReadyOutcome = SessionProgress | { kind: 'gone' } | { kind: 'timeout' };

export class ShellSession {
  private connector: ShellConnector | undefined;
  private session: SessionRef | undefined;
  private watching: AbortController | undefined;
  private attempt = 0;
  private state: ShellState = { phase: 'idle' };
  private command: string[] = SHELL_COMMAND;

  constructor(
    private readonly deps: ShellSessionDeps,
    private readonly options: ShellSessionOptions
  ) {}

  get current(): ShellState {
    return this.state;
  }

  async open(containerName: string, command: string[], size: () => TerminalSize): Promise<void> {
    this.release();
    this.command = command;
    const attempt = ++this.attempt;
    const live = () => attempt === this.attempt;
    this.setState({ phase: 'starting' });

    try {
      const connector = this.deps.createConnector();
      this.connector = connector;
      const clientPublicKey = await connector.load();
      if (!live()) return;

      const session = await this.deps.create({
        instanceName: this.options.instanceName,
        instanceUid: this.options.instanceUid,
        containerName,
        command,
        clientPublicKey,
      });
      if (!live()) {
        void this.deps.remove(session.name);
        return;
      }
      this.session = session;
      this.setState({ phase: 'waiting' });

      const outcome = await this.awaitReady(session, live);
      if (!live()) return;
      switch (outcome.kind) {
        case 'ended':
          return this.end(describeEnding(outcome, this.command));
        case 'gone':
          return this.end(await this.recoverEnding(session.uid));
        case 'timeout':
          return this.end('The shell took too long to start. Try again.');
        case 'pending':
          return;
      }

      this.setState({ phase: 'connecting' });
      connector.connect(
        { uid: session.uid, ...outcome.connection },
        size(),
        (bytes) => {
          if (live()) this.options.onOutput(bytes);
        },
        (ending) => {
          if (live()) void this.finish(ending, session, live);
        },
        () => {
          if (live() && this.state.phase === 'connecting') {
            this.setState({ phase: 'connected' });
          }
        }
      );
    } catch (err) {
      if (live()) this.end(errorMessage(err));
    }
  }

  write(data: string): void {
    if (this.state.phase === 'connecting' || this.state.phase === 'connected') {
      this.connector?.write(data);
    }
  }

  resize(size: TerminalSize): void {
    if (this.state.phase === 'connecting' || this.state.phase === 'connected') {
      this.connector?.resize(size);
    }
  }

  close(): void {
    this.attempt++;
    this.release();
    this.setState({ phase: 'idle' });
  }

  dispose(): void {
    this.attempt++;
    this.release();
  }

  // Sessions are deleted as soon as they end, so a watch is the one reliable
  // way to see how an unready session ended. A broken watch is read again and
  // resumed; a session that is gone by then is explained from its events.
  private async awaitReady(session: CreatedSession, live: () => boolean): Promise<ReadyOutcome> {
    const deadline = this.deps.now() + READY_TIMEOUT_MS;
    let resourceVersion = session.resourceVersion;
    let progress = sessionProgress(session.initial);

    while (live() && progress.kind === 'pending') {
      const remaining = deadline - this.deps.now();
      if (remaining <= 0) return { kind: 'timeout' };

      const watching = new AbortController();
      this.watching = watching;
      let timedOut = false;
      void this.deps.wait(remaining, watching.signal).then(() => {
        if (watching.signal.aborted) return;
        timedOut = true;
        watching.abort();
      });
      try {
        for await (const event of this.deps.watch(session.name, resourceVersion, watching.signal)) {
          if (event.type === 'ERROR') break;
          resourceVersion = event.object.metadata?.resourceVersion ?? resourceVersion;
          if (event.type === 'BOOKMARK') continue;
          progress = sessionProgress(event.object);
          if (event.type === 'DELETED' && progress.kind !== 'ended') return { kind: 'gone' };
          if (progress.kind !== 'pending') break;
        }
      } catch {
        // A broken watch falls through to reading the session again.
      } finally {
        watching.abort();
        if (this.watching === watching) this.watching = undefined;
      }
      if (!live() || progress.kind !== 'pending') break;
      if (timedOut) return { kind: 'timeout' };

      try {
        const raw = await this.deps.get(session.name);
        resourceVersion = raw.metadata?.resourceVersion ?? '';
        progress = sessionProgress(raw);
      } catch (err) {
        if (isGone(err)) return { kind: 'gone' };
        throw err;
      }
      if (progress.kind === 'pending') await this.deps.wait(POLL_INTERVAL_MS);
    }
    return progress;
  }

  private async recoverEnding(uid: string): Promise<string> {
    const ending = await this.deps.ending(uid).catch(() => undefined);
    return ending ? describeEnding(ending, this.command) : SESSION_GONE_MESSAGE;
  }

  // A stream that broke without an ending is explained from the session's
  // status, or from its events once it is gone, as datumctl does.
  private async finish(ending: ConsoleEnding, session: SessionRef, live: () => boolean) {
    if (ending.reason || !ending.error || ending.error === 'closed') {
      return this.end(endingMessage(ending, this.command));
    }
    let message = endingMessage(ending, this.command);
    try {
      const ended = sessionEnding(await this.deps.get(session.name));
      if (ended) message = describeEnding(ended, this.command);
    } catch (err) {
      if (isGone(err)) message = await this.recoverEnding(session.uid);
    }
    if (live()) this.end(message);
  }

  private end(message: string): void {
    this.attempt++;
    this.release();
    this.setState({ phase: 'ended', message });
  }

  private release(): void {
    this.watching?.abort();
    this.watching = undefined;
    this.connector?.dispose();
    this.connector = undefined;
    if (this.session) void this.deps.remove(this.session.name);
    this.session = undefined;
  }

  private setState(state: ShellState): void {
    this.state = state;
    this.options.onState(state);
  }
}
