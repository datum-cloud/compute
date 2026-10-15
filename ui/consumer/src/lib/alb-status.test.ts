import { describe, expect, test } from 'bun:test';
import { albStatus, albStatusDisplay, anyAlbProvisioning, workloadUrlState } from './alb-status';
import type { ConnectedAlb, ResourceCondition } from './api';

const T0 = '2026-10-05T12:00:00Z';
const t0 = new Date(T0).getTime();
const MIN = 60_000;

function cond(
  type: string,
  status: ResourceCondition['status'],
  reason?: string,
  message?: string
): ResourceCondition {
  return { type, status, reason, message, lastTransitionTime: T0 };
}

const programmed = cond('Programmed', 'True', 'Programmed');
const accepted = cond('Accepted', 'True', 'Accepted');
const serviceReady = { name: 'web', ready: cond('Ready', 'True', 'Ready') };

function alb(conditions: ResourceCondition[], services = [serviceReady]): ConnectedAlb {
  return {
    proxyName: 'web',
    displayName: 'web',
    customHostnames: [],
    createdAt: new Date(T0),
    conditions,
    services,
  };
}

const serving = { workloadServing: true, now: t0 + MIN };

describe('albStatus', () => {
  test('ready once programmed with a ready service', () => {
    expect(albStatus(alb([accepted, programmed]), serving)).toEqual({ phase: 'ready' });
  });

  test('a labelled proxy with no service behind it is judged on its own conditions', () => {
    expect(albStatus(alb([accepted, programmed], []), serving)).toEqual({ phase: 'ready' });
  });

  test('configuring until programmed', () => {
    expect(albStatus(alb([]), serving)).toMatchObject({
      phase: 'provisioning',
      step: 'Configuring load balancer',
      stalled: false,
    });
    expect(
      albStatus(alb([cond('Accepted', 'Unknown', 'Pending'), cond('Programmed', 'False', 'Pending')]), serving)
    ).toMatchObject({ phase: 'provisioning', step: 'Configuring load balancer' });
  });

  test('configuring stalls after the control-plane window', () => {
    const status = albStatus(alb([cond('Programmed', 'False', 'Pending')]), {
      workloadServing: true,
      now: t0 + 6 * MIN,
    });
    expect(status).toMatchObject({ phase: 'provisioning', stalled: true });
    expect(albStatusDisplay(status)).toEqual({
      tone: 'warning',
      label: 'Provisioning ALB',
      short: 'Provisioning',
      detail: 'Configuring load balancer — taking longer than usual',
    });
  });

  test('waits for instances while the service has no healthy members', () => {
    for (const reason of ['NoMatchingInterfaces', 'NoServingLocations']) {
      const status = albStatus(
        alb([accepted, programmed], [{ name: 'web', ready: cond('Ready', 'False', reason, 'none') }]),
        { workloadServing: false, now: t0 + 60 * MIN }
      );
      // The workload isn't serving, so its own deploy status owns the slow start.
      expect(status).toMatchObject({ phase: 'provisioning', step: 'Waiting for instances', stalled: false });
    }
  });

  test('waiting for instances stalls once the workload serves', () => {
    const status = albStatus(
      alb([accepted, programmed], [{ name: 'web', ready: cond('Ready', 'False', 'NoServingLocations') }]),
      { workloadServing: true, now: t0 + 6 * MIN }
    );
    expect(status).toMatchObject({ phase: 'provisioning', step: 'Waiting for instances', stalled: true });
  });

  test('a service with no Ready condition yet is still coming up', () => {
    expect(albStatus(alb([accepted, programmed], [{ name: 'web' }]), serving)).toMatchObject({
      phase: 'provisioning',
      step: 'Waiting for instances',
    });
  });

  test('issuing a certificate, with the longer window', () => {
    const pending = alb([accepted, programmed, cond('CertificatesReady', 'False', 'CertificatesPending')]);
    expect(albStatus(pending, { workloadServing: true, now: t0 + 20 * MIN })).toMatchObject({
      phase: 'provisioning',
      step: 'Issuing TLS certificate',
      stalled: false,
    });
    expect(albStatus(pending, { workloadServing: true, now: t0 + 31 * MIN })).toMatchObject({ stalled: true });
  });

  test('a defaulted 1970 transition is timed from creation, not flagged as stalled', () => {
    const status = albStatus(
      alb([
        {
          type: 'Programmed',
          status: 'Unknown',
          reason: 'Pending',
          message: 'Waiting for controller',
          lastTransitionTime: '1970-01-01T00:00:00Z',
        },
      ]),
      { workloadServing: false, now: t0 + 2_000 }
    );
    expect(status).toMatchObject({ phase: 'provisioning', since: new Date(T0), stalled: false });
  });

  test('times from creation when the condition has no transition', () => {
    const status = albStatus(alb([{ type: 'Programmed', status: 'False', reason: 'Pending' }]), serving);
    expect(status).toMatchObject({ phase: 'provisioning', since: new Date(T0) });
  });

  test.each([
    ['rejected', [cond('Accepted', 'False', 'Invalid', 'bad spec')], 'bad spec'],
    ['backend missing', [accepted, cond('Programmed', 'False', 'NetworkServiceBackendNotFound', 'no svc')], 'no svc'],
    [
      'pending that will never clear',
      [accepted, cond('Programmed', 'False', 'Pending', 'The HTTPProxy cannot be programmed: conflict')],
      'The HTTPProxy cannot be programmed: conflict',
    ],
    ['certificate failed', [accepted, programmed, cond('CertificatesReady', 'False', 'CertificatesFailed', 'acme')], 'acme'],
  ] as const)('%s is an error', (_, conditions, message) => {
    const status = albStatus(alb([...conditions]), serving);
    expect(status).toEqual({ phase: 'error', message });
    expect(albStatusDisplay(status)).toMatchObject({ tone: 'danger', label: 'ALB error', detail: message });
  });

  test('a service selecting more than one network is an error', () => {
    const status = albStatus(
      alb([accepted, programmed], [{ name: 'web', ready: cond('Ready', 'False', 'MultipleNetworks', 'two networks') }]),
      serving
    );
    expect(status).toEqual({ phase: 'error', message: 'two networks' });
  });
});

describe('anyAlbProvisioning', () => {
  test('true only while one is still coming up', () => {
    expect(anyAlbProvisioning([alb([accepted, programmed])])).toBe(false);
    expect(anyAlbProvisioning([alb([accepted, programmed]), alb([])])).toBe(true);
    expect(anyAlbProvisioning([alb([cond('Accepted', 'False', 'Invalid')])])).toBe(false);
  });
});

describe('workloadUrlState', () => {
  test('links only once the workload and its load balancer serve', () => {
    expect(workloadUrlState(true, { phase: 'ready' })).toEqual({ live: true });
    expect(workloadUrlState(true, undefined)).toEqual({ live: true });
    expect(workloadUrlState(false, { phase: 'ready' })).toEqual({ live: false, status: 'Not serving yet' });
  });

  test('a load balancer that is not ready explains itself first', () => {
    const provisioning = albStatus(alb([]), serving);
    expect(workloadUrlState(false, provisioning)).toEqual({
      live: false,
      status: 'Provisioning load balancer',
    });
    expect(workloadUrlState(true, { phase: 'error', message: 'x' })).toEqual({
      live: false,
      status: 'Load balancer not serving',
    });
  });
});
