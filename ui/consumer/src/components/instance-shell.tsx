import { createWorkerConnector } from '../lib/console-client/connector';
import type { TerminalSize } from '../lib/console-client/wasm-adapter';
import {
  createSession,
  deleteSession,
  fetchSessionEnding,
  getSession,
  watchSession,
} from '../lib/console-sessions';
import { initialContainer, shellStatusBadge } from '../lib/instance-shell';
import { DEFAULT_SHELL_COMMAND, parseCommand, QUICK_COMMANDS } from '../lib/shell-command';
import {
  popOutFromTab,
  shellWindowHref,
  shellWindowName,
  type WindowStart,
} from '../lib/shell-popout';
import { ShellSession, type ShellSessionDeps, type ShellState } from '../lib/shell-session';
import type { Instance } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import { Button } from '@datum-cloud/datum-ui/button';
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@datum-cloud/datum-ui/card';
import { Icon } from '@datum-cloud/datum-ui/icons';
import { Input } from '@datum-cloud/datum-ui/input';
import { Label } from '@datum-cloud/datum-ui/label';
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@datum-cloud/datum-ui/select';
import { FitAddon } from '@xterm/addon-fit';
import { Terminal } from '@xterm/xterm';
import xtermCss from '@xterm/xterm/css/xterm.css?inline';
import { SquareArrowOutUpRightIcon } from 'lucide-react';
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
const TERMINAL_HEIGHT = 480;
const MIN_WINDOW_TERMINAL_HEIGHT = 240;
const DEFAULT_SIZE: TerminalSize = { cols: 80, rows: 24 };

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
  style.textContent = xtermCss;
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

function ShellStatusBadge({ state }: { state: ShellState }) {
  const badge = shellStatusBadge(state);
  return (
    <Badge type={badge.type} theme={badge.theme} data-testid="compute-plugin-shell-badge">
      {badge.label}
    </Badge>
  );
}

function useWindowFillHeight(
  enabled: boolean,
  rootRef: RefObject<HTMLDivElement | null>,
  regionRef: RefObject<HTMLDivElement | null>
): number | undefined {
  const [height, setHeight] = useState<number>();
  useLayoutEffect(() => {
    const root = rootRef.current;
    const region = regionRef.current;
    if (!enabled || !root || !region) return;
    const update = () => {
      const below = root.getBoundingClientRect().bottom - region.getBoundingClientRect().bottom;
      const top = region.getBoundingClientRect().top;
      setHeight(Math.max(MIN_WINDOW_TERMINAL_HEIGHT, Math.floor(window.innerHeight - top - below)));
    };
    update();
    const observer = new ResizeObserver(update);
    observer.observe(root);
    window.addEventListener('resize', update);
    return () => {
      observer.disconnect();
      window.removeEventListener('resize', update);
    };
  }, [enabled, rootRef, regionRef]);
  return height;
}

interface StartPanelProps {
  containers: string[];
  container: string | undefined;
  onContainer(name: string): void;
  command: string;
  onCommand(text: string): void;
  commandError: string | undefined;
  running: boolean;
  onConnect(): void;
  onPopOut?: () => void;
  compact?: boolean;
}

function StartPanel({
  containers,
  container,
  onContainer,
  command,
  onCommand,
  commandError,
  running,
  onConnect,
  onPopOut,
  compact,
}: StartPanelProps) {
  const containerId = useId();
  const commandId = useId();
  const errorId = useId();
  const canConnect = running && !!container && !commandError;

  const submit = (event: FormEvent) => {
    event.preventDefault();
    if (canConnect) onConnect();
  };

  return (
    <form
      onSubmit={submit}
      aria-label="Start a shell"
      className={compact ? 'flex flex-col gap-3' : 'flex flex-col gap-3 rounded-lg border p-4'}
      data-testid="compute-plugin-shell-start">
      <div className="flex flex-wrap items-start gap-3">
        <div className="flex min-w-0 flex-col gap-1.5" style={{ flex: '0 1 16rem' }}>
          <Label htmlFor={containerId}>Container</Label>
          <Select value={container ?? ''} onValueChange={onContainer}>
            <SelectTrigger
              id={containerId}
              className="bg-card h-9 w-full"
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
        </div>
        <div className="flex min-w-0 flex-col gap-1.5" style={{ flex: '1 1 18rem' }}>
          <Label htmlFor={commandId}>Command</Label>
          <Input
            id={commandId}
            value={command}
            onChange={(event) => onCommand(event.target.value)}
            placeholder={DEFAULT_SHELL_COMMAND}
            autoComplete="off"
            spellCheck={false}
            className="h-9 font-mono"
            aria-invalid={!!commandError}
            aria-describedby={commandError ? errorId : undefined}
            data-testid="compute-plugin-shell-command"
          />
          <div className="flex flex-wrap items-center gap-1.5">
            {QUICK_COMMANDS.map((quick) => (
              <Button
                key={quick}
                htmlType="button"
                type="secondary"
                theme={command.trim() === quick ? 'light' : 'outline'}
                size="xs"
                className="font-mono"
                aria-pressed={command.trim() === quick}
                onClick={() => onCommand(quick)}>
                {quick}
              </Button>
            ))}
            {commandError && (
              <p id={errorId} role="alert" className="text-destructive text-xs">
                {commandError}
              </p>
            )}
          </div>
        </div>
        <div className="ml-auto flex flex-col gap-1.5">
          <Label aria-hidden className="invisible">
            Actions
          </Label>
          <div className="flex flex-wrap items-center justify-end gap-2">
            <Button
              htmlType="submit"
              type="primary"
              theme="solid"
              size="small"
              className="h-9"
              disabled={!canConnect}
              data-testid="compute-plugin-shell-open">
              Connect
            </Button>
            {onPopOut && (
              <Button
                htmlType="button"
                type="secondary"
                theme="outline"
                size="small"
                className="h-9"
                disabled={!canConnect}
                icon={<Icon icon={SquareArrowOutUpRightIcon} size={12} />}
                onClick={onPopOut}
                data-testid="compute-plugin-shell-pop-out">
                Open in new window
              </Button>
            )}
          </div>
        </div>
      </div>
      {!running && (
        <p className="text-muted-foreground text-xs">
          A shell can be opened once the instance is running.
        </p>
      )}
    </form>
  );
}

function SessionLine({
  state,
  container,
  command,
  onClose,
}: {
  state: ShellState;
  container: string;
  command: string;
  onClose(): void;
}) {
  const status = state.phase === 'idle' || state.phase === 'ended' ? '' : STATUS[state.phase];
  return (
    <div
      className="flex min-w-0 flex-wrap items-center justify-between gap-x-4 gap-y-2"
      data-testid="compute-plugin-shell-session">
      <p className="min-w-0 truncate text-sm">
        <span className="text-muted-foreground">Container </span>
        <span className="font-mono">{container}</span>
        <span className="text-muted-foreground"> · </span>
        <span className="font-mono">{command}</span>
      </p>
      <div className="flex items-center gap-2">
        <p
          role="status"
          aria-live="polite"
          className="text-muted-foreground text-xs"
          data-testid="compute-plugin-shell-status">
          {status}
        </p>
        <ShellStatusBadge state={state} />
        {state.phase !== 'ended' && (
          <Button type="secondary" theme="outline" size="small" onClick={onClose}>
            Close shell
          </Button>
        )}
      </div>
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
  const [running, setRunning] = useState<{ container: string; command: string }>();
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
  const fillHeight = useWindowFillHeight(inWindow && showTerminal, rootRef, regionRef);

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
    terminalRef.current = terminal;
    terminal.focus();
    return () => {
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
    if (props.variant !== 'tab') return;
    setPopOutError(
      popOutFromTab({
        opener: window,
        href: shellWindowHref(props.shellHref, { container, command }),
        name: shellWindowName(projectId, instance.name),
        closeTabSession: () => sessionRef.current?.close(),
      })
    );
  };

  const panel = !active && (
    <StartPanel
      containers={instance.containers}
      container={container}
      onContainer={setContainer}
      command={command}
      onCommand={setCommand}
      commandError={commandError}
      running={instanceRunning}
      onConnect={() => connect()}
      onPopOut={inWindow ? undefined : popOut}
      compact={inWindow}
    />
  );

  const endedNotice = ended && (
    <p
      role="alert"
      className="bg-muted text-foreground rounded-md border px-3 py-2 text-sm"
      data-testid="compute-plugin-shell-ended">
      {state.message}
    </p>
  );

  const sessionLine = running && (active || (ended && reachedShell)) && (
    <SessionLine
      state={state}
      container={running.container}
      command={running.command}
      onClose={() => sessionRef.current?.close()}
    />
  );

  const terminalRegion = showTerminal ? (
    <div
      ref={regionRef}
      role="region"
      aria-label={`Shell in container ${running?.container ?? ''}`}
      className={inWindow ? 'overflow-hidden' : 'overflow-hidden rounded-md border'}
      style={{
        height: inWindow ? (fillHeight ?? TERMINAL_HEIGHT) : TERMINAL_HEIGHT,
        padding: 8,
        background: '#000',
      }}>
      <div ref={hostRef} style={{ height: '100%', width: '100%' }} />
    </div>
  ) : (
    !ended && (
      <div
        className="text-muted-foreground flex items-center justify-center rounded-lg border border-dashed px-4 py-10 text-sm"
        data-testid="compute-plugin-shell-empty">
        Choose a container and command, then connect.
      </div>
    )
  );

  if (props.variant === 'window') {
    return (
      <div
        ref={rootRef}
        className="flex min-w-0 flex-col"
        data-testid="compute-plugin-instance-shell">
        <header className="border-b px-4 py-2">{props.title}</header>
        {(panel || endedNotice || sessionLine) && (
          <div className="flex flex-col gap-3 border-b px-4 py-3">
            {panel}
            {endedNotice}
            {sessionLine}
          </div>
        )}
        {showTerminal && terminalRegion}
      </div>
    );
  }

  return (
    <Card className="bg-card" data-testid="compute-plugin-instance-shell">
      <CardHeader>
        <CardTitle>Shell</CardTitle>
        <CardDescription>
          Run a command in a container of this instance. The session ends when you close it or leave
          the page.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        {popOutError && (
          <p
            role="alert"
            className="text-destructive text-sm"
            data-testid="compute-plugin-shell-pop-out-error">
            {popOutError}
          </p>
        )}
        {panel}
        {endedNotice}
        {sessionLine}
        {terminalRegion}
      </CardContent>
    </Card>
  );
}
