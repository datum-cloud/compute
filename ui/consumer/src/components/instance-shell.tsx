import { createWorkerConnector } from '../lib/console-client/connector';
import type { TerminalSize } from '../lib/console-client/wasm-adapter';
import { createSession, deleteSession, getSession } from '../lib/console-sessions';
import { initialContainer } from '../lib/instance-shell';
import { popOutFromTab, shellWindowHref, shellWindowName } from '../lib/shell-popout';
import { ShellSession, type ShellSessionDeps, type ShellState } from '../lib/shell-session';
import type { Instance } from '../schema';
import { Badge } from '@datum-cloud/datum-ui/badge';
import { Button } from '@datum-cloud/datum-ui/button';
import {
  Card,
  CardAction,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@datum-cloud/datum-ui/card';
import { Icon } from '@datum-cloud/datum-ui/icons';
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
  connected: 'Connected',
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
    get: (name) => getSession(projectId, name),
    remove: (name) => deleteSession(projectId, name),
    wait: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
    now: () => Date.now(),
  };
}

function statusBadge(state: ShellState): { label: string; type: 'success' | 'muted' | 'warning' } {
  if (state.phase === 'connected') return { label: 'Connected', type: 'success' };
  if (state.phase === 'ended') return { label: 'Ended', type: 'muted' };
  return { label: 'Opening', type: 'warning' };
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
      setHeight(
        Math.max(MIN_WINDOW_TERMINAL_HEIGHT, Math.floor(window.innerHeight - top - below))
      );
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

function startingContainer(containers: string[], requested?: string): string | undefined {
  if (requested && containers.includes(requested)) return requested;
  return initialContainer(containers);
}

type InstanceShellProps = { projectId: string; instance: Instance } & (
  | { variant: 'tab'; shellHref: string }
  | { variant: 'window'; title: ReactNode; container?: string }
);

export function InstanceShell(props: InstanceShellProps) {
  const { projectId, instance, variant } = props;
  const inWindow = variant === 'window';
  const [container, setContainer] = useState(() =>
    startingContainer(instance.containers, inWindow ? props.container : undefined)
  );
  const [state, setState] = useState<ShellState>({ phase: 'idle' });
  const [popOutError, setPopOutError] = useState<string>();
  const sessionRef = useRef<ShellSession | null>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const hostRef = useRef<HTMLDivElement>(null);
  const rootRef = useRef<HTMLDivElement>(null);
  const regionRef = useRef<HTMLDivElement>(null);
  const pickerId = useId();

  useEffect(() => {
    const session = new ShellSession(sessionDeps(projectId), {
      instanceName: instance.name,
      instanceUid: instance.uid,
      onState: setState,
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

  const active = state.phase !== 'idle';
  const fillHeight = useWindowFillHeight(inWindow && active, rootRef, regionRef);

  useEffect(() => {
    const host = hostRef.current;
    if (!active || !host) return;
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
  }, [active]);

  useEffect(() => {
    if (state.phase === 'connected') terminalRef.current?.focus();
  }, [state.phase]);

  const running = instance.status === 'Available';
  const multiple = instance.containers.length > 1;

  const open = () => {
    if (!container) return;
    terminalRef.current?.reset();
    const size = (): TerminalSize => {
      const terminal = terminalRef.current;
      return terminal ? { cols: terminal.cols, rows: terminal.rows } : DEFAULT_SIZE;
    };
    void sessionRef.current?.open(container, size);
  };

  useEffect(() => {
    if (inWindow && running && container) open();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inWindow]);

  const popOut = () => {
    if (props.variant !== 'tab') return;
    setPopOutError(
      popOutFromTab({
        opener: window,
        href: shellWindowHref(props.shellHref, container),
        name: shellWindowName(projectId, instance.name),
        closeTabSession: () => sessionRef.current?.close(),
      })
    );
  };

  const picker = multiple ? (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={pickerId} className="text-sm font-medium">
        Container
      </label>
      <Select
        value={container ?? ''}
        onValueChange={setContainer}
        disabled={active && state.phase !== 'ended'}>
        <SelectTrigger
          id={pickerId}
          className="bg-card h-8 w-auto min-w-48 text-xs"
          data-testid="compute-plugin-shell-container">
          <SelectValue placeholder="Choose a container" />
        </SelectTrigger>
        <SelectContent>
          {instance.containers.map((name) => (
            <SelectItem key={name} value={name} className="text-xs">
              {name}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  ) : (
    <p className="text-muted-foreground text-sm">
      Container <span className="text-foreground font-mono">{container}</span>
    </p>
  );

  const badge = statusBadge(state);
  const statusText =
    state.phase === 'ended'
      ? inWindow
        ? ''
        : state.message
      : state.phase === 'idle'
        ? ''
        : STATUS[state.phase];

  const terminalRegion = active && (
    <div
      ref={regionRef}
      role="region"
      aria-label={`Shell in container ${container ?? ''}`}
      className={inWindow ? 'overflow-hidden' : 'overflow-hidden rounded-md border'}
      style={{
        height: inWindow ? (fillHeight ?? TERMINAL_HEIGHT) : TERMINAL_HEIGHT,
        padding: 8,
        background: '#000',
      }}>
      <div ref={hostRef} style={{ height: '100%', width: '100%' }} />
    </div>
  );

  if (props.variant === 'window') {
    return (
      <div ref={rootRef} className="flex min-w-0 flex-col" data-testid="compute-plugin-instance-shell">
        <header className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b px-4 py-2">
          <div className="min-w-0 flex-1">{props.title}</div>
          <div className="flex items-center gap-3">
            <p
              role="status"
              aria-live="polite"
              className="text-muted-foreground text-xs"
              data-testid="compute-plugin-shell-status">
              {state.phase === 'connected' ? '' : statusText}
            </p>
            {active && (
              <Badge type={badge.type} theme="light">
                {badge.label}
              </Badge>
            )}
            {active && state.phase !== 'ended' && (
              <Button
                type="secondary"
                theme="outline"
                size="small"
                onClick={() => sessionRef.current?.close()}>
                Close shell
              </Button>
            )}
          </div>
        </header>
        {state.phase === 'ended' && (
          <div
            role="alert"
            className="bg-muted flex flex-wrap items-end justify-between gap-3 border-b px-4 py-3"
            data-testid="compute-plugin-shell-ended">
            <p className="text-sm">{state.message}</p>
            <div className="flex flex-wrap items-end gap-3">
              {multiple && picker}
              <Button type="primary" theme="solid" size="small" onClick={open}>
                Open new shell
              </Button>
            </div>
          </div>
        )}
        {!active && (
          <div className="flex flex-col gap-3 p-4">
            {running ? (
              <div className="flex flex-wrap items-end gap-3">
                {picker}
                <Button
                  type="primary"
                  theme="solid"
                  size="small"
                  disabled={!container}
                  onClick={open}
                  data-testid="compute-plugin-shell-open">
                  Open shell
                </Button>
              </div>
            ) : (
              <p className="text-muted-foreground text-sm">
                A shell can be opened once the instance is running.
              </p>
            )}
          </div>
        )}
        {terminalRegion}
      </div>
    );
  }

  return (
    <Card className="bg-card" data-testid="compute-plugin-instance-shell">
      <CardHeader>
        <CardTitle>Shell</CardTitle>
        <CardDescription>
          Run commands in a container of this instance. The shell ends when you leave or close this
          page.
        </CardDescription>
        <CardAction className="flex items-center gap-2">
          {active && (
            <Badge type={badge.type} theme="light">
              {badge.label}
            </Badge>
          )}
          {active &&
            (state.phase === 'ended' ? (
              <Button type="secondary" theme="solid" size="small" onClick={open}>
                Open new shell
              </Button>
            ) : (
              <Button
                type="secondary"
                theme="outline"
                size="small"
                onClick={() => sessionRef.current?.close()}>
                Close shell
              </Button>
            ))}
          <Button
            type="secondary"
            theme="outline"
            size="small"
            icon={<Icon icon={SquareArrowOutUpRightIcon} size={12} />}
            onClick={popOut}
            data-testid="compute-plugin-shell-pop-out">
            Open in new window
          </Button>
        </CardAction>
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
        {!active && (
          <div className="flex flex-wrap items-end gap-3">
            {picker}
            <Button
              type="primary"
              theme="solid"
              size="small"
              disabled={!container || !running}
              onClick={open}
              data-testid="compute-plugin-shell-open">
              Open shell
            </Button>
          </div>
        )}
        {!active && !running && (
          <p className="text-muted-foreground text-sm">
            A shell can be opened once the instance is running.
          </p>
        )}
        {active && multiple && state.phase === 'ended' && picker}
        <p
          role="status"
          aria-live="polite"
          className="text-muted-foreground text-sm"
          data-testid="compute-plugin-shell-status">
          {statusText}
        </p>
        {terminalRegion}
      </CardContent>
    </Card>
  );
}
