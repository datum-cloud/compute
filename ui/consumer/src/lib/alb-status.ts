/**
 * Whether a workload's Application Load Balancer is serving yet, from the
 * HTTPProxy's conditions and those of the NetworkService behind it.
 *
 * The classification mirrors network-services-operator's condition catalog
 * (`internal/agent/catalog.go`): which reasons are in-flight states that clear
 * on their own, how long each should plausibly take, and which need someone to
 * act. Two of its NetworkService reasons — nothing matching the selector, and
 * no location serving — are filed there as faults, but they are exactly what a
 * service reports while the workload behind it is still coming up, so here
 * they read as "waiting for instances" until the workload serves.
 */
import type { ConnectedAlb, ResourceCondition } from './api';
import type { StatusDisplay } from './workload-presenters';

/** The operator's expected windows (`windowControlPlaneRead`, `windowCertificate`). */
const CONTROL_PLANE_WINDOW_MS = 5 * 60_000;
const CERTIFICATE_WINDOW_MS = 30 * 60_000;

/** Prefix the operator puts on a Programmed=Pending message it will never clear. */
const CANNOT_PROGRAM_PREFIX = 'The HTTPProxy cannot be programmed:';

/** NetworkService reasons that mean "nothing behind it can take traffic yet". */
const WAITING_FOR_MEMBERS = new Set(['NoMatchingInterfaces', 'NoServingLocations']);

export type AlbStatus =
  | { phase: 'ready' }
  | {
      phase: 'provisioning';
      /** What it is waiting on, e.g. "Issuing TLS certificate". */
      step: string;
      message?: string;
      since?: Date;
      /** Past the step's expected window. */
      stalled: boolean;
    }
  | { phase: 'error'; message: string };

function find(conditions: readonly ResourceCondition[], type: string) {
  return conditions.find((c) => c.type === type);
}

/**
 * When a step started: its condition's last transition, but never before the
 * object existed. The networking CRDs default every condition to a 1970
 * transition ("Waiting for controller"), which would otherwise read as decades
 * overdue the moment an ALB is created.
 */
function sinceOf(condition: ResourceCondition | undefined, createdAt?: Date): Date | undefined {
  const t = condition?.lastTransitionTime ? new Date(condition.lastTransitionTime) : undefined;
  if (!t || Number.isNaN(t.getTime())) return createdAt;
  return createdAt && createdAt > t ? createdAt : t;
}

/**
 * @param workloadServing the workload behind the ALB has ready instances. Until
 *   it does, an ALB waiting on its members is waiting on the workload, whose own
 *   deploy status already flags a slow start — so it is never stalled.
 */
export function albStatus(
  alb: ConnectedAlb,
  { workloadServing, now = Date.now() }: { workloadServing: boolean; now?: number }
): AlbStatus {
  const accepted = find(alb.conditions, 'Accepted');
  const programmed = find(alb.conditions, 'Programmed');
  const certificates = find(alb.conditions, 'CertificatesReady');

  const provisioning = (
    step: string,
    condition: ResourceCondition | undefined,
    windowMs: number | undefined,
    message?: string
  ): AlbStatus => {
    const since = sinceOf(condition, alb.createdAt);
    return {
      phase: 'provisioning',
      step,
      message,
      since,
      stalled: windowMs !== undefined && !!since && now - since.getTime() > windowMs,
    };
  };

  // Faults first: waiting on anything else won't help while one of these holds.
  if (accepted?.status === 'False' && accepted.reason !== 'Pending') {
    return { phase: 'error', message: accepted.message || accepted.reason || 'The load balancer was rejected' };
  }
  if (programmed?.status === 'False') {
    const message = programmed.message?.trim() ?? '';
    if (programmed.reason !== 'Pending' || message.startsWith(CANNOT_PROGRAM_PREFIX)) {
      return { phase: 'error', message: message || programmed.reason || 'The load balancer could not be programmed' };
    }
  }
  const brokenService = alb.services.find(
    (svc) => svc.ready?.status === 'False' && !WAITING_FOR_MEMBERS.has(svc.ready.reason ?? '')
  );
  if (brokenService?.ready) {
    return { phase: 'error', message: brokenService.ready.message || brokenService.ready.reason || 'Not ready' };
  }
  if (certificates?.status === 'False' && certificates.reason === 'CertificatesFailed') {
    return { phase: 'error', message: certificates.message || 'A TLS certificate could not be issued' };
  }

  if (programmed?.status !== 'True') {
    return provisioning('Configuring load balancer', programmed ?? accepted, CONTROL_PLANE_WINDOW_MS, programmed?.message);
  }
  const waiting = alb.services.find((svc) => svc.ready?.status !== 'True');
  if (waiting) {
    return provisioning(
      'Waiting for instances',
      waiting.ready,
      workloadServing ? CONTROL_PLANE_WINDOW_MS : undefined,
      waiting.ready?.message
    );
  }
  if (certificates?.status === 'False') {
    return provisioning('Issuing TLS certificate', certificates, CERTIFICATE_WINDOW_MS, certificates.message);
  }
  return { phase: 'ready' };
}

/**
 * Pill wording for an ALB that isn't serving yet; undefined once it is. The
 * label stays put while it provisions — only the tone turns to a warning past
 * the expected window — and the specifics go in `detail`, for a tooltip.
 */
export function albStatusDisplay(
  status: AlbStatus
): (StatusDisplay & { short: string; detail: string }) | undefined {
  if (status.phase === 'ready') return undefined;
  if (status.phase === 'error') {
    return { tone: 'danger', label: 'ALB error', short: 'Error', detail: status.message };
  }
  const detail = [status.step, status.stalled ? 'taking longer than usual' : undefined, status.message]
    .filter(Boolean)
    .join(' — ');
  return {
    tone: status.stalled ? 'warning' : 'info',
    label: 'Provisioning ALB',
    short: 'Provisioning',
    detail,
  };
}

/** Any of these ALBs still coming up — used to poll faster. */
export function anyAlbProvisioning(albs: readonly ConnectedAlb[]): boolean {
  return albs.some((alb) => albStatus(alb, { workloadServing: false }).phase === 'provisioning');
}
