import type { FromWorker, ToWorker } from './messages';
import { loadConsoleClient, type ConsoleClient, type ConsoleStream } from './wasm-adapter';

interface WorkerScope {
  postMessage(message: FromWorker, transfer: Transferable[]): void;
  onmessage: ((event: MessageEvent<ToWorker>) => void) | null;
}

const scope = self as unknown as WorkerScope;

let client: ConsoleClient | undefined;
let stream: ConsoleStream | undefined;

function post(message: FromWorker, transfer: Transferable[] = []) {
  scope.postMessage(message, transfer);
}

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

scope.onmessage = async (event: MessageEvent<ToWorker>) => {
  const message = event.data;
  try {
    switch (message.type) {
      case 'load':
        client = await loadConsoleClient(message.assets);
        post({ type: 'loaded', publicKey: client.publicKey() });
        break;
      case 'connect':
        if (!client) throw new Error('The shell client is not loaded.');
        stream = client.connect(
          message.target,
          message.size,
          (bytes) => {
            const copy = bytes.slice();
            post({ type: 'output', bytes: copy }, [copy.buffer]);
          },
          (ending) => post({ type: 'end', ending }),
          () => post({ type: 'connected' })
        );
        break;
      case 'input':
        stream?.write(message.data);
        break;
      case 'resize':
        stream?.resize(message.size);
        break;
      case 'close':
        stream?.close();
        break;
    }
  } catch (err) {
    post({ type: 'failed', message: errorMessage(err) });
  }
};
