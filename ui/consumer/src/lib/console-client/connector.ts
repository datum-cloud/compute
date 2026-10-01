import type { FromWorker, ToWorker } from './messages';
import type { ConsoleAssets, ConsoleEnding, ConsoleTarget, TerminalSize } from './wasm-adapter';

export interface ShellConnector {
  load(): Promise<string>;
  connect(
    target: ConsoleTarget,
    size: TerminalSize,
    onOutput: (bytes: Uint8Array) => void,
    onEnd: (ending: ConsoleEnding) => void,
    onConnected: () => void
  ): void;
  write(data: string): void;
  resize(size: TerminalSize): void;
  dispose(): void;
}

export const CLOSE_TIMEOUT_MS = 2_000;

export interface ShellWorker {
  postMessage(message: ToWorker): void;
  terminate(): void;
  onmessage: ((event: MessageEvent<FromWorker>) => void) | null;
  onerror: ((event: ErrorEvent) => void) | null;
}

function spawnShellWorker(): ShellWorker {
  return new Worker(new URL('./shell.worker.ts', import.meta.url), {
    name: 'instance-shell',
  }) as ShellWorker;
}

export function createWorkerConnector(
  assets: ConsoleAssets | null,
  spawn: () => ShellWorker = spawnShellWorker,
  closeTimeoutMs = CLOSE_TIMEOUT_MS
): ShellConnector {
  const worker = spawn();
  const send = (message: ToWorker) => worker.postMessage(message);
  let loaded: ((publicKey: string) => void) | undefined;
  let failed: ((err: Error) => void) | undefined;
  let onOutput: ((bytes: Uint8Array) => void) | undefined;
  let onEnd: ((ending: ConsoleEnding) => void) | undefined;
  let onConnected: (() => void) | undefined;
  let streaming = false;
  let ended = false;
  let done = false;
  let disposed = false;
  let afterEnd: (() => void) | undefined;

  const finish = (ending: ConsoleEnding) => {
    ended = true;
    afterEnd?.();
    if (done) return;
    done = true;
    onEnd?.(ending);
  };

  const fail = (message: string) => {
    if (failed) {
      failed(new Error(message));
      failed = undefined;
      loaded = undefined;
    } else if (onEnd) {
      finish({ exitCode: 1, reason: '', message: '', error: message });
    }
  };

  worker.onmessage = (event: MessageEvent<FromWorker>) => {
    const message = event.data;
    switch (message.type) {
      case 'loaded':
        loaded?.(message.publicKey);
        loaded = undefined;
        failed = undefined;
        break;
      case 'connected':
        onConnected?.();
        break;
      case 'output':
        onOutput?.(message.bytes);
        break;
      case 'end':
        finish(message.ending);
        break;
      case 'failed':
        fail(message.message);
        break;
    }
  };
  worker.onerror = (event) => {
    event.preventDefault();
    ended = true;
    afterEnd?.();
    fail('The shell client stopped unexpectedly.');
  };

  return {
    load() {
      return new Promise<string>((resolve, reject) => {
        if (!assets) {
          reject(new Error('The shell client is not part of this build.'));
          return;
        }
        loaded = resolve;
        failed = reject;
        send({ type: 'load', assets });
      });
    },
    connect(target, size, output, end, connected) {
      onOutput = output;
      onEnd = end;
      onConnected = connected;
      streaming = true;
      send({ type: 'connect', target, size });
    },
    write(data) {
      send({ type: 'input', data });
    },
    resize(size) {
      send({ type: 'resize', size });
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      done = true;
      onOutput = undefined;
      onEnd = undefined;
      onConnected = undefined;
      loaded = undefined;
      failed = undefined;
      if (!streaming || ended) {
        worker.terminate();
        return;
      }
      const timer = setTimeout(() => worker.terminate(), closeTimeoutMs);
      afterEnd = () => {
        clearTimeout(timer);
        worker.terminate();
      };
      send({ type: 'close' });
    },
  };
}
