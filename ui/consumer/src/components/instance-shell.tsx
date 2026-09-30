import { createWorkerConnector } from '../lib/console-client/connector';
import type { TerminalSize } from '../lib/console-client/wasm-adapter';
import { createSession, deleteSession, getSession } from '../lib/console-sessions';
import { initialContainer } from '../lib/instance-shell';
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
import { useEffect, useId, useRef, useState } from 'react';

const XTERM_STYLE_ID = 'compute-plugin-xterm-css';
const TERMINAL_HEIGHT = 480;
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
    createConnector: () => createWorkerConnector(),
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

export function InstanceShell({ projectId, instance }: { projectId: string; instance: Instance }) {
  const [container, setContainer] = useState(() => initialContainer(instance.containers));
  const [state, setState] = useState<ShellState>({ phase: 'idle' });
  const sessionRef = useRef<ShellSession | null>(null);
  const terminalRef = useRef<Terminal | null>(null);
  const hostRef = useRef<HTMLDivElement>(null);
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

  return (
    <Card className="bg-card" data-testid="compute-plugin-instance-shell">
      <CardHeader>
        <CardTitle>Shell</CardTitle>
        <CardDescription>
          Run commands in a container of this instance. The shell closes when you leave this page.
        </CardDescription>
        {active && (
          <CardAction className="flex items-center gap-2">
            <Badge type={badge.type} theme="light">
              {badge.label}
            </Badge>
            {state.phase === 'ended' ? (
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
            )}
          </CardAction>
        )}
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
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
          {state.phase === 'ended'
            ? state.message
            : state.phase === 'idle'
              ? ''
              : STATUS[state.phase]}
        </p>
        {active && (
          <div
            role="region"
            aria-label={`Shell in container ${container ?? ''}`}
            className="overflow-hidden rounded-md border"
            style={{ height: TERMINAL_HEIGHT, padding: 8, background: '#000' }}>
            <div ref={hostRef} style={{ height: '100%', width: '100%' }} />
          </div>
        )}
      </CardContent>
    </Card>
  );
}
