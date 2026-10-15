export const DEFAULT_SHELL_COMMAND = '/bin/sh';

export const QUICK_COMMANDS = ['/bin/sh', '/bin/bash'] as const;

export const MAX_COMMAND_ARGS = 64;
export const MAX_COMMAND_BYTES = 16 * 1024;

export type ParsedCommand = { ok: true; argv: string[] } | { ok: false; error: string };

const DOUBLE_QUOTE_ESCAPES = new Set(['"', '\\', '$', '`']);

// parseCommand splits a command line into arguments the way a POSIX shell
// splits words: whitespace separates them, single quotes keep everything
// literally, double quotes keep everything but a few backslash escapes, and a
// backslash outside quotes keeps the next character. Nothing is expanded, so
// $HOME, * and ~ reach the command as typed.
export function parseCommand(text: string): ParsedCommand {
  const argv: string[] = [];
  let word = '';
  let inWord = false;
  let quote: "'" | '"' | undefined;

  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quote === "'") {
      if (c === "'") quote = undefined;
      else word += c;
      continue;
    }
    if (quote === '"') {
      if (c === '"') {
        quote = undefined;
      } else if (c === '\\' && i + 1 < text.length && DOUBLE_QUOTE_ESCAPES.has(text[i + 1])) {
        word += text[++i];
      } else {
        word += c;
      }
      continue;
    }
    if (/\s/.test(c)) {
      if (inWord) argv.push(word);
      word = '';
      inWord = false;
      continue;
    }
    inWord = true;
    if (c === "'" || c === '"') {
      quote = c;
    } else if (c === '\\') {
      if (i + 1 >= text.length) return { ok: false, error: 'The command ends with a backslash.' };
      word += text[++i];
    } else {
      word += c;
    }
  }

  if (quote) return { ok: false, error: `Close the ${quote === "'" ? 'single' : 'double'} quote.` };
  if (inWord) argv.push(word);
  if (argv.length === 0) return { ok: false, error: 'Enter a command, such as /bin/sh.' };
  if (argv.length > MAX_COMMAND_ARGS) {
    return { ok: false, error: `A command can have at most ${MAX_COMMAND_ARGS} arguments.` };
  }
  const bytes = argv.reduce((n, arg) => n + new TextEncoder().encode(arg).length, 0);
  if (bytes > MAX_COMMAND_BYTES) return { ok: false, error: 'The command is longer than 16 KiB.' };
  return { ok: true, argv };
}
