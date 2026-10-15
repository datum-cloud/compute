export interface ConsoleAssets {
  wasmUrl: string;
  runtimeUrl: string;
}

export interface ConsoleTarget {
  uid: string;
  endpointID: string;
  relayURLs: string[];
  target: string;
}

export interface TerminalSize {
  cols: number;
  rows: number;
}

export interface ConsoleEnding {
  exitCode: number;
  reason: string;
  message: string;
  error: string;
}

export interface ConsoleStream {
  write(data: string | Uint8Array): void;
  resize(size: TerminalSize): void;
  close(): void;
}

export interface ConsoleClient {
  publicKey(): string;
  connect(
    target: ConsoleTarget,
    size: TerminalSize,
    onOutput: (bytes: Uint8Array) => void,
    onEnd: (ending: ConsoleEnding) => void,
    onConnected: () => void
  ): ConsoleStream;
}

interface GoRuntime {
  importObject: WebAssembly.Imports;
  run(instance: WebAssembly.Instance): Promise<void>;
}

interface DatumExec {
  features?: string[];
  publicKey(): string;
  connect(
    session: ConsoleTarget & Partial<TerminalSize>,
    onOutput: (bytes: Uint8Array, fd: number) => void,
    onEnd: (ending: Partial<ConsoleEnding>) => void,
    onConnected?: () => void
  ): {
    write(data: string | Uint8Array): void;
    resize(cols: number, rows: number): void;
    close(): void;
  };
}

interface ConsoleScope {
  importScripts(url: string): void;
  Go?: new () => GoRuntime;
  datumExec?: DatumExec;
}

const PUBLIC_KEY = /^[0-9a-f]{64}$/;

async function instantiate(
  url: string,
  imports: WebAssembly.Imports
): Promise<WebAssembly.Instance> {
  const res = await fetch(url);
  if (!res.ok) throw new Error(`The shell client could not be downloaded (${res.status}).`);
  if (res.headers.get('content-type')?.startsWith('application/wasm')) {
    return (await WebAssembly.instantiateStreaming(res, imports)).instance;
  }
  return (await WebAssembly.instantiate(await res.arrayBuffer(), imports)).instance;
}

export async function loadConsoleClient(
  assets: ConsoleAssets,
  scope: ConsoleScope = globalThis as unknown as ConsoleScope
): Promise<ConsoleClient> {
  scope.importScripts(assets.runtimeUrl);
  if (!scope.Go) throw new Error('The shell client runtime did not load.');
  const go = new scope.Go();
  void go.run(await instantiate(assets.wasmUrl, go.importObject));

  const exec = scope.datumExec;
  if (!exec) throw new Error('The shell client did not start.');
  const signalsConnected = Array.isArray(exec.features) && exec.features.includes('connected');

  return {
    publicKey() {
      const key = exec.publicKey();
      if (!PUBLIC_KEY.test(key)) throw new Error('The shell client produced an invalid key.');
      return key;
    },
    connect(target, size, onOutput, onEnd, onConnected) {
      let connected = false;
      const markConnected = () => {
        if (connected) return;
        connected = true;
        onConnected();
      };
      const session = {
        uid: target.uid,
        endpointID: target.endpointID,
        relayURLs: target.relayURLs,
        target: target.target,
        cols: size.cols,
        rows: size.rows,
      };
      const output = (bytes: Uint8Array) => {
        markConnected();
        onOutput(bytes);
      };
      const end = (ending: Partial<ConsoleEnding>) =>
        onEnd({
          exitCode: ending.exitCode ?? 1,
          reason: ending.reason ?? '',
          message: ending.message ?? '',
          error: ending.error ?? '',
        });
      const stream = signalsConnected
        ? exec.connect(session, output, end, markConnected)
        : exec.connect(session, output, end);
      return {
        write: (data) => stream.write(data),
        resize: ({ cols, rows }) => stream.resize(cols, rows),
        close: () => stream.close(),
      };
    },
  };
}
