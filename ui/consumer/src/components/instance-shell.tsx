import { createWorkerConnector } from '../lib/console-client/connector';
import type { TerminalSize } from '../lib/console-client/wasm-adapter';
import {
  createSession,
  deleteSession,
  fetchSessionEnding,
  getSession,
  watchSession,
} from '../lib/console-sessions';
import {
  initialContainer,
  shellStatusBadge,
  type ShellStatusBadgeStyle,
} from '../lib/instance-shell';
import { DEFAULT_SHELL_COMMAND, parseCommand, QUICK_COMMANDS } from '../lib/shell-command';
import {
  shellLayout,
  terminalHeight,
  TERMINAL_INPUT_CSS,
  type ShellLayout,
} from '../lib/shell-layout';
import {
  createHandoff,
  discardHandoff,
  handoffStorage,
  handoffTarget,
  popOutFromTab,
  shellWindowHref,
  shellWindowName,
  type WindowStart,
} from '../lib/shell-popout';
import { ShellSession, type ShellSessionDeps, type ShellState } from '../lib/shell-session';
import type { Instance } from '../schema';
import { Button } from '@datum-cloud/datum-ui/button';
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from '@datum-cloud/datum-ui/dropdown';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { InputGroup, InputGroupAddon, InputGroupButton } from '@datum-cloud/datum-ui/input-group';
import { Label } from '@datum-cloud/datum-ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@datum-cloud/datum-ui/select';
import { Spinner } from '@datum-cloud/datum-ui/spinner';
import { Tooltip } from '@datum-cloud/datum-ui/tooltip';
import { FitAddon } from '@xterm/addon-fit';
import { Terminal } from '@xterm/xterm';
import xtermCss from '@xterm/xterm/css/xterm.css?inline';
import { ChevronDownIcon, SquareArrowOutUpRightIcon, SquareTerminalIcon } from 'lucide-react';
import {
  useEffect,
  useId,
  useLayoutEffect,
  useRef,
  useState,
  type FormEvent,
  type ReactNode,
  type RefObject,
} from 'react';
import { consoleClientAssets } from 'virtual:console-client-assets';

const XTERM_STYLE_ID = 'compute-plugin-xterm-css';
const DEFAULT_SIZE: TerminalSize = { cols: 80, rows: 24 };

// A toolbar sits in its own bar above the terminal, except in the shell
// window, where it shares the window's header row.
const TOOLBAR_FRAME = 'border-b px-3 py-2';
const IN_HEADER = 'min-w-0 flex-1';

// Room for a shell and a few arguments. The field scrolls for anything longer,
// so the toolbar doesn't stretch it across the page.
const COMMAND_WIDTH = '20rem';

// The command field draws no frame of its own inside the input group. Inline,
// so no portal rule can put the datum-ui Input's border back.
const BARE_INPUT = {
  border: 0,
  outline: 'none',
  boxShadow: 'none',
  background: 'transparent',
};

// Text on the terminal's black background. The portal only generates the
// Tailwind classes it uses itself, and it has no white-on-black ones.
const ON_TERMINAL = {
  text: '#fff',
  body: 'rgba(255, 255, 255, 0.85)',
  muted: 'rgba(255, 255, 255, 0.55)',
  faint: 'rgba(255, 255, 255, 0.4)',
  rule: 'rgba(255, 255, 255, 0.15)',
};

const STATUS: Record<Exclude<ShellState['phase'], 'idle' | 'ended'>, string> = {
  starting: 'Starting the shell client…',
  waiting: 'Preparing the session…',
  connecting: 'Connecting…',
  connected: '',
};

function ensureXtermStyles() {
  if (document.getElementById(XTERM_STYLE_ID)) return;
  const style = document.createElement('style');
  style.id = XTERM_STYLE_ID;
  style.textContent = `${xtermCss}\n${TERMINAL_INPUT_CSS}`;
  document.head.appendChild(style);
}

function sessionDeps(projectId: string): ShellSessionDeps {
  return {
    createConnector: () => createWorkerConnector(consoleClientAssets),
    create: (input) => createSession(projectId, input),
    watch: (name, resourceVersion, signal) =>
      watchSession(projectId, name, resourceVersion, signal),
    get: (name) => getSession(projectId, name),
    ending: (uid) => fetchSessionEnding(projectId, uid),
    remove: (name) => deleteSession(projectId, name),
    wait: (ms, signal) =>
      new Promise((resolve) => {
        const timer = setTimeout(resolve, ms);
        signal?.addEventListener('abort', () => {
          clearTimeout(timer);
          resolve();
        });
      }),
    now: () => Date.now(),
  };
}

const STATUS_DOT: Record<ShellStatusBadgeStyle['type'], string> = {
  success: '#22c55e',
  warning: '#f59e0b',
  muted: '#a1a1aa',
};

function ShellStatusDot({ state }: { state: ShellState }) {
  const badge = shellStatusBadge(state);
  return (
    <span
      role="img"
      aria-label={badge.label}
      title={badge.label}
      className="shrink-0"
      style={{ width: 8, height: 8, borderRadius: 9999, background: STATUS_DOT[badge.type] }}
      data-testid="compute-plugin-shell-badge"
    />
  );
}

function viewportSize() {
  const vv = window.visualViewport;
  return {
    width: window.innerWidth,
    height: vv?.height ?? window.innerHeight,
    coarse: window.matchMedia?.('(pointer: coarse)').matches ?? false,
  };
}

function onViewportChange(update: () => void): () => void {
  const vv = window.visualViewport;
  window.addEventListener('resize', update);
  window.addEventListener('orientationchange', update);
  vv?.addEventListener('resize', update);
  return () => {
    window.removeEventListener('resize', update);
    window.removeEventListener('orientationchange', update);
    vv?.removeEventListener('resize', update);
  };
}

function useViewport() {
  const [size, setSize] = useState(viewportSize);
  useEffect(() => onViewportChange(() => setSize(viewportSize())), []);
  return size;
}

function useTerminalHeight(
  inWindow: boolean,
  narrow: boolean,
  viewportHeight: number,
  rootRef: RefObject<HTMLDivElement | null>,
  regionRef: RefObject<HTMLDivElement | null>
): number {
  const [edges, setEdges] = useState({ top: 0, below: 0 });
  useLayoutEffect(() => {
    const root = rootRef.current;
    const region = regionRef.current;
    if (!inWindow || !root || !region) return;
    const update = () => {
      const below = root.getBoundingClientRect().bottom - region.getBoundingClientRect().bottom;
      setEdges({ top: region.getBoundingClientRect().top, below });
    };
    update();
    const observer = new ResizeObserver(update);
    observer.observe(root);
    const stop = onViewportChange(update);
    return () => {
      observer.disconnect();
      stop();
    };
  }, [inWindow, rootRef, regionRef]);
  return terminalHeight({ inWindow, narrow, viewportHeight, ...edges });
}

interface StartToolbarProps {
  containers: string[];
  container: string | undefined;
  onContainer(name: string): void;
  command: string;
  onCommand(text: string): void;
  commandError: string | undefined;
  popOutError: string | undefined;
  running: boolean;
  onConnect(): void;
  onPopOut?: () => void;
  layout: ShellLayout;
  frame?: string;
}

function StartToolbar({
  containers,
  container,
  onContainer,
  command,
  onCommand,
  commandError,
  popOutError,
  running,
  onConnect,
  onPopOut,
  layout,
  frame = TOOLBAR_FRAME,
}: StartToolbarProps) {
  const control = layout.controlHeight ? { height: layout.controlHeight } : undefined;
  const target = layout.controlHeight ? { minHeight: layout.controlHeight } : undefined;
  const containerId = useId();
  const commandId = useId();
  const errorId = useId();
  const canConnect = running && !!container && !commandError;
  const error = commandError ?? popOutError;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (canConnect) onConnect();
  };

  return (
    <form
      onSubmit={submit}
      aria-label="Start a shell"
      className={`flex flex-col gap-2 ${frame}`}
      data-testid="compute-plugin-shell-start">
      <div className={layout.narrow ? 'flex flex-col gap-2' : 'flex items-center gap-3'}>
        <div className="flex min-w-0 shrink-0 items-center gap-2">
          <Label htmlFor={containerId} className="text-muted-foreground text-xs font-normal">
            Container
          </Label>
          {containers.length === 1 ? (
            <span
              id={containerId}
              className="truncate font-mono text-sm"
              data-testid="compute-plugin-shell-container">
              {containers[0]}
            </span>
          ) : (
            <Select value={container ?? ''} onValueChange={onContainer}>
              <SelectTrigger
                id={containerId}
                className={layout.narrow ? 'bg-card h-8 flex-1' : 'bg-card h-8 w-44'}
                style={control}
                data-testid="compute-plugin-shell-container">
                <SelectValue placeholder="Choose a container" />
              </SelectTrigger>
              <SelectContent>
                {containers.map((name) => (
                  <SelectItem key={name} value={name}>
                    {name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
        </div>
        <InputGroup
          className="bg-card h-8 min-w-0"
          style={{ ...control, ...(layout.narrow ? undefined : { width: COMMAND_WIDTH }) }}>
          <InputGroupAddon aria-hidden className="font-mono">
            $
          </InputGroupAddon>
          <Label htmlFor={commandId} className="sr-only">
            Command
          </Label>
          <input
            id={commandId}
            data-slot="input-group-control"
            value={command}
            onChange={(event) => onCommand(event.target.value)}
            placeholder={DEFAULT_SHELL_COMMAND}
            autoComplete="off"
            spellCheck={false}
            className="placeholder:text-muted-foreground h-full min-w-0 flex-1 px-2 font-mono text-sm"
            style={BARE_INPUT}
            aria-invalid={!!commandError}
            aria-describedby={commandError ? errorId : undefined}
            data-testid="compute-plugin-shell-command"
          />
          <InputGroupAddon align="inline-end">
            <DropdownMenu>
              <DropdownMenuTrigger asChild>
                <InputGroupButton
                  size="icon-xs"
                  aria-label="Choose a shell"
                  data-testid="compute-plugin-shell-presets">
                  <ChevronDownIcon />
                </InputGroupButton>
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">
                <DropdownMenuRadioGroup value={command.trim()} onValueChange={onCommand}>
                  {QUICK_COMMANDS.map((quick) => (
                    <DropdownMenuRadioItem key={quick} value={quick} className="font-mono">
                      {quick}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
              </DropdownMenuContent>
            </DropdownMenu>
          </InputGroupAddon>
        </InputGroup>
        <div
          className={
            layout.narrow ? 'flex items-center gap-2' : 'ml-auto flex shrink-0 items-center gap-2'
          }>
          {onPopOut && layout.showPopOut && (
            <Tooltip message="Open in new window">
              <span className="inline-flex">
                <Button
                  htmlType="button"
                  type="secondary"
                  theme="outline"
                  size="small"
                  className="h-8"
                  aria-label="Open in new window"
                  disabled={!canConnect}
                  icon={<Icon icon={SquareArrowOutUpRightIcon} size={14} />}
                  onClick={onPopOut}
                  data-testid="compute-plugin-shell-pop-out"
                />
              </span>
            </Tooltip>
          )}
          <Button
            htmlType="submit"
            type="primary"
            theme="solid"
            size="small"
            className="h-8"
            block={layout.narrow}
            style={target}
            disabled={!canConnect}
            data-testid="compute-plugin-shell-open">
            Connect
          </Button>
        </div>
      </div>
      {error && (
        <p
          id={commandError ? errorId : undefined}
          role="alert"
          className="text-destructive text-xs"
          data-testid={commandError ? undefined : 'compute-plugin-shell-pop-out-error'}>
          {error}
        </p>
      )}
    </form>
  );
}

function SessionToolbar({
  state,
  container,
  command,
  onClose,
  layout,
  frame = TOOLBAR_FRAME,
}: {
  state: ShellState;
  container: string;
  command: string;
  onClose(): void;
  layout: ShellLayout;
  frame?: string;
}) {
  const status = state.phase === 'idle' || state.phase === 'ended' ? '' : STATUS[state.phase];
  return (
    <div
      className={`flex min-w-0 items-center gap-2 ${frame}`}
      data-testid="compute-plugin-shell-session">
      <ShellStatusDot state={state} />
      <p className="min-w-0 flex-1 truncate font-mono text-sm" title={`${container} · ${command}`}>
        {container}
        <span className="text-muted-foreground"> · {command}</span>
      </p>
      <p
        role="status"
        aria-live="polite"
        className={layout.narrow ? 'sr-only' : 'text-muted-foreground shrink-0 text-xs'}
        data-testid="compute-plugin-shell-status">
        {status}
      </p>
      <Button
        type="secondary"
        theme="borderless"
        size="small"
        className="h-8 shrink-0"
        style={layout.controlHeight ? { minHeight: layout.controlHeight } : undefined}
        onClick={onClose}>
        Close shell
      </Button>
    </div>
  );
}

// IdleScreen fills the terminal frame before a session reaches the shell, so
// the page keeps one shape from choosing a command through to typing in it.
function IdleScreen({
  running,
  ended,
  container,
  command,
}: {
  running: boolean;
  ended: string | undefined;
  container: string | undefined;
  command: string;
}) {
  let title: ReactNode;
  let detail: ReactNode;
  if (ended) {
    title = "Couldn't open the shell";
    detail = ended;
  } else if (!running) {
    title = "This instance isn't running";
    detail = 'A shell can be opened once the instance is running.';
  } else {
    title = (
      <>
        Connect to run{' '}
        <span className="font-mono" style={{ color: ON_TERMINAL.text }}>
          {command || DEFAULT_SHELL_COMMAND}
        </span>
        {container && (
          <>
            {' '}
            in{' '}
            <span className="font-mono" style={{ color: ON_TERMINAL.text }}>
              {container}
            </span>
          </>
        )}
      </>
    );
    detail = 'The session ends when you close it or leave the page.';
  }
  return (
    <div
      role={ended ? 'alert' : undefined}
      className="flex h-full flex-col items-center justify-center gap-1 px-4 text-center"
      data-testid={ended ? 'compute-plugin-shell-ended' : 'compute-plugin-shell-empty'}>
      <Icon
        icon={SquareTerminalIcon}
        size={20}
        className="mb-2"
        style={{ color: ON_TERMINAL.faint }}
      />
      <p className="text-sm" style={{ color: ON_TERMINAL.body }}>
        {title}
      </p>
      <p className="text-xs" style={{ color: ON_TERMINAL.muted }}>
        {detail}
      </p>
    </div>
  );
}

type InstanceShellProps = { projectId: string; instance: Instance } & (
  | { variant: 'tab'; shellHref: string }
  | { variant: 'window'; title: ReactNode; start: WindowStart }
);

export function InstanceShell(props: InstanceShellProps) {
  const { projectId, instance, variant } = props;
  const inWindow = variant === 'window';
  const [container, setContainer] = useState(() =>
    inWindow ? props.start.container : initialContainer(instance.containers)
  );
  const [command, setCommand] = useState(() =>
    inWindow ? props.start.command : DEFAULT_SHELL_COMMAND
  );
  const [running, setRunning] = useState<{
    container: string;
    command: string;
  }>();
  const [state, setState] = useState<ShellState>({ phase: 'idle' });
  const [reachedShell, setReachedShell] = useState(false);
  const [popOutError, setPopOutError] = useState<string>();
  const sessionRef = useRef<ShellSession | null>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const hostRef = useRef<HTMLDivElement>(null);
  const rootRef = useRef<HTMLDivElement>(null);
  const regionRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const session = new ShellSession(sessionDeps(projectId), {
      instanceName: instance.name,
      instanceUid: instance.uid,
      onState: (next) => {
        if (next.phase === 'connected') setReachedShell(true);
        setState(next);
      },
      onOutput: (bytes) => terminalRef.current?.write(bytes),
    });
    sessionRef.current = session;
    const onPageHide = () => session.dispose();
    window.addEventListener('pagehide', onPageHide);
    return () => {
      window.removeEventListener('pagehide', onPageHide);
      session.dispose();
      sessionRef.current = null;
    };
  }, [projectId, instance.name, instance.uid]);

  const parsed = parseCommand(command);
  const commandError = parsed.ok ? undefined : parsed.error;
  const instanceRunning = instance.status === 'Available';
  const ended = state.phase === 'ended';
  const active = state.phase !== 'idle' && !ended;
  const showTerminal = active || (ended && reachedShell);
  const viewport = useViewport();
  const layout = shellLayout(viewport.width, viewport.coarse);
  const height = useTerminalHeight(inWindow, layout.narrow, viewport.height, rootRef, regionRef);

  useEffect(() => {
    const host = hostRef.current;
    if (!showTerminal || !host) return;
    ensureXtermStyles();
    const terminal = new Terminal({
      cursorBlink: true,
      fontSize: 13,
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace',
      scrollback: 5000,
    });
    const fit = new FitAddon();
    terminal.loadAddon(fit);
    terminal.open(host);
    fit.fit();
    const input = terminal.onData((data) => sessionRef.current?.write(data));
    const resize = terminal.onResize((size) => sessionRef.current?.resize(size));
    const observer = new ResizeObserver(() => fit.fit());
    observer.observe(host);
    const keepPromptVisible = () => {
      fit.fit();
      if (host.contains(document.activeElement)) {
        terminal.scrollToBottom();
        host.scrollIntoView({ block: 'end' });
      }
    };
    const stopViewport = onViewportChange(keepPromptVisible);
    terminalRef.current = terminal;
    terminal.focus();
    return () => {
      stopViewport();
      observer.disconnect();
      input.dispose();
      resize.dispose();
      terminal.dispose();
      terminalRef.current = null;
    };
  }, [showTerminal]);

  useEffect(() => {
    if (state.phase === 'connected') terminalRef.current?.focus();
  }, [state.phase]);

  const connect = (target = container, text = command) => {
    const argv = parseCommand(text);
    if (!target || !argv.ok) return;
    terminalRef.current?.reset();
    setReachedShell(false);
    setRunning({ container: target, command: text.trim() });
    const size = (): TerminalSize => {
      const terminal = terminalRef.current;
      return terminal ? { cols: terminal.cols, rows: terminal.rows } : DEFAULT_SIZE;
    };
    void sessionRef.current?.open(target, argv.argv, size);
  };

  useEffect(() => {
    if (inWindow && instanceRunning && props.start.connect) {
      connect(props.start.container, props.start.command);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inWindow]);

  const popOut = () => {
    if (props.variant !== 'tab' || !container) return;
    const storage = handoffStorage();
    const nonce = storage
      ? createHandoff(
          storage,
          {
            target: handoffTarget(projectId, instance.name),
            container,
            command,
          },
          Date.now()
        )
      : undefined;
    const error = popOutFromTab({
      opener: window,
      href: shellWindowHref(props.shellHref, nonce),
      name: shellWindowName(projectId, instance.name),
      closeTabSession: () => sessionRef.current?.close(),
    });
    if (error && storage && nonce) discardHandoff(storage, nonce);
    setPopOutError(error);
  };

  const toolbar =
    active && running ? (
      <SessionToolbar
        state={state}
        container={running.container}
        command={running.command}
        onClose={() => sessionRef.current?.close()}
        layout={layout}
        frame={inWindow ? IN_HEADER : undefined}
      />
    ) : (
      <StartToolbar
        containers={instance.containers}
        container={container}
        onContainer={setContainer}
        command={command}
        onCommand={setCommand}
        commandError={commandError}
        popOutError={popOutError}
        running={instanceRunning}
        onConnect={() => connect()}
        onPopOut={inWindow ? undefined : popOut}
        layout={layout}
        frame={inWindow ? IN_HEADER : undefined}
      />
    );

  const endedMessage = ended ? state.message : undefined;
  const opening =
    state.phase === 'starting' || state.phase === 'waiting' || state.phase === 'connecting'
      ? STATUS[state.phase]
      : undefined;

  const terminalRegion = (
    <div
      ref={regionRef}
      role="region"
      aria-label={running ? `Shell in container ${running.container}` : 'Shell'}
      className="relative flex flex-col overflow-hidden"
      onClick={() => terminalRef.current?.focus()}
      style={{ height, width: '100%', maxWidth: '100%', background: '#000' }}>
      {showTerminal ? (
        <>
          <div className="min-h-0 flex-1" style={{ padding: 8 }}>
            <div ref={hostRef} style={{ height: '100%', width: '100%' }} />
          </div>
          {opening && (
            <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
              <p className="flex items-center gap-2 text-sm" style={{ color: ON_TERMINAL.muted }}>
                <Spinner className="size-4" />
                {opening}
              </p>
            </div>
          )}
          {endedMessage && (
            <p
              role="alert"
              className="shrink-0 border-t px-3 py-2 text-sm"
              style={{ color: ON_TERMINAL.body, borderColor: ON_TERMINAL.rule }}
              data-testid="compute-plugin-shell-ended">
              {endedMessage}
            </p>
          )}
        </>
      ) : (
        <IdleScreen
          running={instanceRunning}
          ended={endedMessage}
          container={container}
          command={command.trim()}
        />
      )}
    </div>
  );

  if (props.variant === 'window') {
    return (
      <div
        ref={rootRef}
        className="flex min-w-0 flex-col"
        data-testid="compute-plugin-instance-shell">
        <header className="flex flex-wrap items-center gap-x-6 gap-y-2 border-b px-4 py-2">
          <div className="min-w-0">{props.title}</div>
          {toolbar}
        </header>
        {terminalRegion}
      </div>
    );
  }

  return (
    <section
      aria-label="Shell"
      className="bg-card overflow-hidden rounded-lg border"
      data-testid="compute-plugin-instance-shell">
      {toolbar}
      {terminalRegion}
    </section>
  );
}
