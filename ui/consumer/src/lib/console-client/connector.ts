import type { FromWorker, ToWorker } from './messages';
import type { ConsoleAssets, ConsoleEnding, ConsoleTarget, TerminalSize } from './wasm-adapter';
import { consoleClientAssets } from 'virtual:console-client-assets';

export interface ShellConnector {
  load(): Promise<string>;
  connect(
    target: ConsoleTarget,
    size: TerminalSize,
    onOutput: (bytes: Uint8Array) => void,
    onEnd: (ending: ConsoleEnding) => void
  ): void;
  write(data: string): void;
  resize(size: TerminalSize): void;
  dispose(): void;
}

export const consoleClientBundled = consoleClientAssets !== null;

export function createWorkerConnector(
  assets: ConsoleAssets | null = consoleClientAssets
): ShellConnector {
  const worker = new Worker(new URL('./shell.worker.ts', import.meta.url), {
    name: 'instance-shell',
  });
  const send = (message: ToWorker) => worker.postMessage(message);
  let loaded: ((publicKey: string) => void) | undefined;
  let failed: ((err: Error) => void) | undefined;
  let onOutput: ((bytes: Uint8Array) => void) | undefined;
  let onEnd: ((ending: ConsoleEnding) => void) | undefined;
  let done = false;

  const finish = (ending: ConsoleEnding) => {
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
    connect(target, size, output, end) {
      onOutput = output;
      onEnd = end;
      send({ type: 'connect', target, size });
    },
    write(data) {
      send({ type: 'input', data });
    },
    resize(size) {
      send({ type: 'resize', size });
    },
    dispose() {
      done = true;
      onOutput = undefined;
      onEnd = undefined;
      loaded = undefined;
      failed = undefined;
      worker.terminate();
    },
  };
}
