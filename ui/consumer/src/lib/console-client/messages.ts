import type { ConsoleAssets, ConsoleEnding, ConsoleTarget, TerminalSize } from './wasm-adapter';

export type ToWorker =
  | { type: 'load'; assets: ConsoleAssets }
  | { type: 'connect'; target: ConsoleTarget; size: TerminalSize }
  | { type: 'input'; data: string }
  | { type: 'resize'; size: TerminalSize };

export type FromWorker =
  | { type: 'loaded'; publicKey: string }
  | { type: 'output'; bytes: Uint8Array }
  | { type: 'end'; ending: ConsoleEnding }
  | { type: 'failed'; message: string };
