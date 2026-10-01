import { describe, expect, test } from 'bun:test';
import {
  MIN_TERMINAL_HEIGHT,
  TERMINAL_HEIGHT,
  TERMINAL_INPUT_CSS,
  TOUCH_TARGET_PX,
  shellLayout,
  terminalHeight,
} from './shell-layout';

describe('shellLayout', () => {
  test('stacks, enlarges controls and keeps the shell in the page on a phone', () => {
    expect(shellLayout(390)).toEqual({ narrow: true, showPopOut: false, controlHeight: 44 });
    expect(shellLayout(640).narrow).toBe(true);
  });

  test('keeps the desktop layout above 640px', () => {
    expect(shellLayout(641)).toEqual({ narrow: false, showPopOut: true, controlHeight: undefined });
  });

  test('keeps a touch device in the page with large targets, even in landscape', () => {
    expect(shellLayout(915, true)).toEqual({ narrow: false, showPopOut: false, controlHeight: 44 });
  });

  test('uses touch targets of at least 44px', () => {
    expect(TOUCH_TARGET_PX).toBeGreaterThanOrEqual(44);
  });
});

describe('terminalHeight', () => {
  test('keeps the fixed height on a wide screen', () => {
    expect(terminalHeight({ inWindow: false, narrow: false, viewportHeight: 300 })).toBe(
      TERMINAL_HEIGHT
    );
  });

  test('shrinks with the visual viewport when the keyboard opens', () => {
    const closed = terminalHeight({ inWindow: false, narrow: true, viewportHeight: 844 });
    const open = terminalHeight({ inWindow: false, narrow: true, viewportHeight: 480 });
    expect(closed).toBe(TERMINAL_HEIGHT);
    expect(open).toBeLessThan(480);
    expect(open).toBeGreaterThanOrEqual(MIN_TERMINAL_HEIGHT);
  });

  test('never shrinks below a usable height', () => {
    expect(terminalHeight({ inWindow: false, narrow: true, viewportHeight: 250 })).toBe(
      MIN_TERMINAL_HEIGHT
    );
  });

  test('fills a window below its header', () => {
    expect(
      terminalHeight({ inWindow: true, narrow: true, viewportHeight: 700, top: 150, below: 0 })
    ).toBe(550);
  });
});

describe('TERMINAL_INPUT_CSS', () => {
  test('keeps the terminal input at 16px so iOS does not zoom', () => {
    expect(TERMINAL_INPUT_CSS).toContain('xterm-helper-textarea');
    expect(TERMINAL_INPUT_CSS).toMatch(/font-size:\s*16px/);
  });
});
