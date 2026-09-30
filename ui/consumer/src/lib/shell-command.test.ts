import { describe, expect, test } from 'bun:test';
import { MAX_COMMAND_ARGS, parseCommand } from './shell-command';

function argv(text: string): string[] {
  const parsed = parseCommand(text);
  if (!parsed.ok) throw new Error(parsed.error);
  return parsed.argv;
}

function error(text: string): string {
  const parsed = parseCommand(text);
  if (parsed.ok) throw new Error(`parsed ${JSON.stringify(parsed.argv)}`);
  return parsed.error;
}

describe('parseCommand', () => {
  test('splits on whitespace', () => {
    expect(argv('  /bin/bash   -l\t-i ')).toEqual(['/bin/bash', '-l', '-i']);
  });

  test('keeps single-quoted text literally', () => {
    expect(argv(`sh -c 'echo "$HOME" \\ done'`)).toEqual(['sh', '-c', 'echo "$HOME" \\ done']);
  });

  test('honours only the POSIX escapes inside double quotes', () => {
    expect(argv(`echo "a \\"b\\" \\$c \\\\ \\n"`)).toEqual(['echo', 'a "b" $c \\ \\n']);
  });

  test('keeps the character after a backslash outside quotes', () => {
    expect(argv('ls my\\ file \\$HOME')).toEqual(['ls', 'my file', '$HOME']);
  });

  test('joins adjacent quoted and bare parts into one argument', () => {
    expect(argv(`--name='a b'"c"d`)).toEqual(['--name=a bcd']);
  });

  test('keeps an empty quoted argument', () => {
    expect(argv(`printf '%s|' '' ""`)).toEqual(['printf', '%s|', '', '']);
  });

  test('evaluates nothing', () => {
    expect(argv('echo $(id) `id` ~ * ; rm -rf / && true | cat > out')).toEqual([
      'echo',
      '$(id)',
      '`id`',
      '~',
      '*',
      ';',
      'rm',
      '-rf',
      '/',
      '&&',
      'true',
      '|',
      'cat',
      '>',
      'out',
    ]);
  });

  test('refuses an empty command', () => {
    expect(error('   ')).toMatch(/Enter a command/);
  });

  test('refuses an unterminated quote', () => {
    expect(error(`sh -c 'echo`)).toBe('Close the single quote.');
    expect(error('echo "hi')).toBe('Close the double quote.');
  });

  test('refuses a trailing backslash', () => {
    expect(error('ls \\')).toMatch(/backslash/);
  });

  test('refuses more arguments than a session takes', () => {
    expect(error(Array.from({ length: MAX_COMMAND_ARGS + 1 }, () => 'x').join(' '))).toMatch(
      /at most 64 arguments/
    );
  });

  test('refuses a command longer than a session takes', () => {
    expect(error(`echo ${'x'.repeat(16 * 1024)}`)).toMatch(/16 KiB/);
  });
});
