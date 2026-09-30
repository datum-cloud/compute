export const NARROW_MAX_WIDTH = 640;
export const TOUCH_TARGET_PX = 44;
export const TERMINAL_HEIGHT = 480;
export const MIN_TERMINAL_HEIGHT = 200;

// iOS zooms the page when a focused input's text is under 16px, and the
// terminal takes its keystrokes through a hidden textarea.
export const TERMINAL_INPUT_CSS = '.xterm .xterm-helper-textarea { font-size: 16px !important; }';

export interface ShellLayout {
  narrow: boolean;
  // A popup on a phone or tablet opens a new tab that duplicates this page
  // and closes this tab's session, so touch devices and small screens keep
  // the shell in the page.
  showPopOut: boolean;
  controlHeight: number | undefined;
}

export function shellLayout(width: number, coarsePointer = false): ShellLayout {
  const narrow = width <= NARROW_MAX_WIDTH;
  const touch = narrow || coarsePointer;
  return {
    narrow,
    showPopOut: !touch,
    controlHeight: touch ? TOUCH_TARGET_PX : undefined,
  };
}

// The space the page keeps around the terminal on a small screen: the
// portal's sticky mobile header, the session line and some room below.
const NARROW_CHROME_PX = 180;

// terminalHeight sizes the terminal to what the user can see. On a small
// screen that is the visual viewport, which shrinks when the keyboard opens,
// so the prompt stays above the keyboard. A pop-out window fills the space
// below its own header.
export function terminalHeight({
  inWindow,
  narrow,
  viewportHeight,
  top = 0,
  below = 0,
}: {
  inWindow: boolean;
  narrow: boolean;
  viewportHeight: number;
  top?: number;
  below?: number;
}): number {
  if (inWindow) {
    return Math.max(MIN_TERMINAL_HEIGHT, Math.floor(viewportHeight - top - below));
  }
  if (!narrow) return TERMINAL_HEIGHT;
  return Math.max(
    MIN_TERMINAL_HEIGHT,
    Math.min(TERMINAL_HEIGHT, Math.floor(viewportHeight - NARROW_CHROME_PX))
  );
}
