import type { ShellConnector } from './console-client/connector';
import type { ConsoleEnding, TerminalSize } from './console-client/wasm-adapter';
import {
  reasonMessage,
  sessionProgress,
  type NewSession,
  type RawConsoleSession,
  type SessionRef,
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
  create(input: NewSession): Promise<SessionRef>;
  get(name: string): Promise<RawConsoleSession>;
  remove(name: string): Promise<void>;
  wait(ms: number): Promise<void>;
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

export function endingMessage(ending: ConsoleEnding): string {
  if (ending.reason) return reasonMessage(ending.reason, ending.message);
  if (ending.error === 'closed') return 'The shell was closed.';
  if (ending.error) {
    return `The connection to the instance was lost (${ending.error}). Open a new shell to try again.`;
  }
  if (ending.exitCode === 0) return 'The shell exited.';
  return `The shell exited with code ${ending.exitCode}.`;
}

function errorMessage(err: unknown): string {
  return err instanceof Error && err.message ? err.message : 'The shell could not be opened.';
}

export class ShellSession {
  private connector: ShellConnector | undefined;
  private session: SessionRef | undefined;
  private attempt = 0;
  private state: ShellState = { phase: 'idle' };

  constructor(
    private readonly deps: ShellSessionDeps,
    private readonly options: ShellSessionOptions
  ) {}

  get current(): ShellState {
    return this.state;
  }

  async open(containerName: string, size: () => TerminalSize): Promise<void> {
    this.release();
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
        clientPublicKey,
      });
      if (!live()) {
        void this.deps.remove(session.name);
        return;
      }
      this.session = session;
      this.setState({ phase: 'waiting' });

      const deadline = this.deps.now() + READY_TIMEOUT_MS;
      for (;;) {
        const progress = sessionProgress(await this.deps.get(session.name));
        if (!live()) return;
        if (progress.kind === 'ended') return this.end(progress.message);
        if (progress.kind === 'ready') {
          this.setState({ phase: 'connecting' });
          connector.connect(
            { uid: session.uid, ...progress.connection },
            size(),
            (bytes) => {
              if (!live()) return;
              if (this.state.phase === 'connecting') this.setState({ phase: 'connected' });
              this.options.onOutput(bytes);
            },
            (ending) => {
              if (live()) this.end(endingMessage(ending));
            }
          );
          return;
        }
        if (this.deps.now() >= deadline) {
          return this.end('The shell took too long to start. Try again.');
        }
        await this.deps.wait(POLL_INTERVAL_MS);
        if (!live()) return;
      }
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

  private end(message: string): void {
    this.attempt++;
    this.release();
    this.setState({ phase: 'ended', message });
  }

  private release(): void {
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
