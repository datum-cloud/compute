/**
 * Plain-language readings of a workload for its Overview: how to reach it
 * privately, what its network policy lets in, and its scale. Kept free of
 * React so each can be tested on its own.
 */
import type { Instance, Workload } from '../schema';

/** An instance's private name on its network, or why it has none yet. */
export type PrivateName =
  | { ready: true; hostname: string }
  | { ready: false; message?: string };

/**
 * The name the platform published for `instance` (`status.dns`), read rather
 * than built: names follow a scheme the platform owns and can change, and the
 * status also says when a location has no private DNS at all. Undefined when
 * the platform hasn't reported anything for the instance.
 */
export function instancePrivateName(instance: Instance): PrivateName | undefined {
  const published = instance.privateNames.find((entry) => entry.ready && entry.hostnames.length > 0);
  if (published) return { ready: true, hostname: published.hostnames[0] };
  const pending = instance.privateNames[0];
  return pending ? { ready: false, message: pending.message } : undefined;
}

/**
 * Why a running instance isn't ready: the message on a `Ready=False`
 * condition when `Available` is True, e.g. its network interface went
 * unprogrammed. `status` only follows `Available`, so without this such an
 * instance reads as healthy. Undefined for a ready instance, and for one
 * still coming up, whose status already says so.
 */
export function instanceNotReady(instance: Instance): string | undefined {
  if (instance.status !== 'Available') return undefined;
  const ready = instance.conditions.find((c) => c.type === 'Ready');
  if (ready?.status !== 'False') return undefined;
  return ready.message || ready.reason || 'Not ready';
}

/** How to reach a private workload, for its status line. */
export type PrivateReach =
  /** One instance with a published name: its address, ready to copy. */
  | { kind: 'address'; address: string }
  /** Several instances, or no published name: the network and port, without picking one. */
  | { kind: 'network'; network?: string; port?: number };

/**
 * One address when the workload has a single instance whose name is
 * published, since that is the one to connect to. With several instances each
 * has its own name, so the status line names only the network and port, and
 * the instances list carries the addresses.
 */
export function privateReach(workload: Workload, instances: readonly Instance[]): PrivateReach {
  const port = workload.namedPorts[0]?.port;
  if (instances.length === 1 && port !== undefined) {
    const name = instancePrivateName(instances[0]);
    if (name?.ready) return { kind: 'address', address: `${name.hostname}:${port}` };
  }
  return { kind: 'network', network: workload.networks[0], port };
}

const ANYWHERE = new Set(['0.0.0.0/0', '::/0']);

/**
 * What a network policy rule lets in, as a sentence: "6379/TCP from anywhere
 * on app-net". "Anywhere" here means anywhere on the network, since the
 * policy only governs traffic that reaches the interface.
 */
export function ingressText(rule: Workload['ingress'][number]): string {
  const ports = rule.ports.length > 0 ? rule.ports.join(', ') : 'Every port';
  const open = rule.from.length === 0 || rule.from.some((cidr) => ANYWHERE.has(cidr));
  const from = open ? 'from anywhere' : `from ${rule.from.join(', ')}`;
  return rule.network ? `${ports} ${from} on ${rule.network}` : `${ports} ${from}`;
}

/** Replica bounds as a phrase: "min 1, no max", "1 to 3", or "2". */
export function scaleText(scale: Workload['scale']): string | undefined {
  if (!scale || scale.min === undefined) return undefined;
  if (scale.max === undefined) return `min ${scale.min}, no max`;
  if (scale.max === scale.min) return String(scale.min);
  return `${scale.min} to ${scale.max}`;
}

/** A container port with its name, when it has one: "6379/TCP (redis)". */
export function portText(port: Workload['namedPorts'][number]): string {
  const base = `${port.port}/${port.protocol}`;
  return port.name ? `${base} (${port.name})` : base;
}
