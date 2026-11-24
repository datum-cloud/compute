/**
 * Network in/out charts on the instance Metrics tab. A label-values probe on
 * the `network` label decides whether the VPC interface traffic series exist
 * for this instance's interface refs before the charts query them.
 */
import { useMemo } from 'react';
import {
  networkInterfaceRefs,
  networkTrafficTotalQuery,
  networkThroughputQuery,
  networkInterfaceSeriesLabels,
  networkTrafficQuery,
  networkTrafficSeriesMatch,
} from './metrics-queries';
import { isPrometheusDenied, usePrometheusLabelValues } from './prometheus';
import { shortInstanceLabels } from './series-view';
import type { Instance, InstanceNetworkInterface } from '../schema';

export interface NetworkInterfaceMetricsState {
  receiveQuery: string | undefined;
  transmitQuery: string | undefined;
  seriesLabels: Record<string, string>;
  /** True when the instance has no interface ref or namespace, or the probe found no series. */
  unavailable: boolean;
  isLoading: boolean;
  isDenied: boolean;
  /** Probe failure other than a denial; shown as the chart's error state. */
  error: unknown;
}

const NO_INTERFACES: readonly InstanceNetworkInterface[] = [];

/** Pure: decide the state from the probe result. Tested without React. */
export function resolveNetworkInterfaceMetrics(input: {
  projectId: string | undefined;
  namespace: string | undefined;
  interfaces: readonly InstanceNetworkInterface[];
  probedNetworks: readonly string[] | undefined; // undefined while loading
  probeDenied: boolean;
  probeError: unknown;
  probeLoading: boolean;
}): NetworkInterfaceMetricsState {
  const { projectId, namespace, interfaces, probedNetworks, probeDenied, probeError, probeLoading } = input;
  const seriesLabels = networkInterfaceSeriesLabels(interfaces);

  if (networkInterfaceRefs(interfaces).length === 0 || !namespace) {
    return {
      receiveQuery: undefined,
      transmitQuery: undefined,
      seriesLabels,
      unavailable: true,
      isLoading: false,
      isDenied: false,
      error: undefined,
    };
  }

  const error = probeDenied || probeLoading ? undefined : probeError;
  const found = !probeDenied && !probeLoading && (probedNetworks?.length ?? 0) > 0;
  return {
    receiveQuery: projectId ? networkTrafficQuery(projectId, namespace, interfaces, 'receive') : undefined,
    transmitQuery: projectId ? networkTrafficQuery(projectId, namespace, interfaces, 'transmit') : undefined,
    seriesLabels,
    unavailable: !probeDenied && !probeLoading && !error && !found,
    isLoading: probeLoading,
    isDenied: probeDenied,
    error,
  };
}

/** Hook: runs the label-values probe (label `network`, match from networkTrafficSeriesMatch) and returns resolveNetworkInterfaceMetrics. */
export function useNetworkInterfaceMetrics(
  projectId: string | undefined,
  instance: Instance | undefined
): NetworkInterfaceMetricsState {
  const interfaces = instance?.networkInterfaces ?? NO_INTERFACES;
  const namespace = instance?.namespace;
  const refs = networkInterfaceRefs(interfaces);
  const match = projectId ? networkTrafficSeriesMatch(projectId, namespace, interfaces) : undefined;
  const enabled = !!projectId && refs.length > 0;
  const probe = usePrometheusLabelValues(projectId, 'network', match, { enabled });
  const probeDenied = isPrometheusDenied(probe.error);
  const probeError = probe.error ?? undefined;
  const probeLoading = enabled && probe.isLoading;
  const probedNetworks = probe.data;
  return useMemo(
    () =>
      resolveNetworkInterfaceMetrics({
        projectId,
        namespace,
        interfaces,
        probedNetworks,
        probeDenied,
        probeError,
        probeLoading,
      }),
    [projectId, namespace, interfaces, probedNetworks, probeDenied, probeError, probeLoading]
  );
}

/**
 * Series labels for a workload's interfaces: the instance's short label
 * ("us-east-1-0"), plus the interface name when an instance has several.
 */
export function workloadInterfaceSeriesLabels(instances: readonly Instance[]): Record<string, string> {
  const short = shortInstanceLabels(instances.map((instance) => instance.name));
  const labels: Record<string, string> = {};
  for (const instance of instances) {
    const refs = instance.networkInterfaces.filter((i) => i.networkInterfaceRef);
    for (const iface of refs) {
      const base = short[instance.name] ?? instance.name;
      labels[iface.networkInterfaceRef as string] = refs.length > 1 ? `${base} · ${iface.name}` : base;
    }
  }
  return labels;
}

export interface WorkloadNetworkMetricsState extends NetworkInterfaceMetricsState {
  /** Every interface summed, for a single headline series. */
  receiveTotalQuery: string | undefined;
  transmitTotalQuery: string | undefined;
  /** In and out together, one series per interface. */
  throughputQuery: string | undefined;
  /** Each instance's interface refs, to fold interface series into instance rows. */
  refsByInstance: Record<string, string[]>;
}

/**
 * Network traffic across a workload's instances: the instance hook's probe
 * and queries over every instance's interfaces, with per-instance series
 * labels and summed totals.
 */
export function useWorkloadNetworkMetrics(
  projectId: string | undefined,
  instances: readonly Instance[]
): WorkloadNetworkMetricsState {
  const interfaces = useMemo(() => instances.flatMap((instance) => instance.networkInterfaces), [instances]);
  const namespace = instances.find((instance) => instance.namespace)?.namespace;
  const match = projectId ? networkTrafficSeriesMatch(projectId, namespace, interfaces) : undefined;
  const enabled = !!projectId && networkInterfaceRefs(interfaces).length > 0;
  const probe = usePrometheusLabelValues(projectId, 'network', match, { enabled });
  const probeDenied = isPrometheusDenied(probe.error);
  const probeError = probe.error ?? undefined;
  const probeLoading = enabled && probe.isLoading;
  const probedNetworks = probe.data;
  return useMemo(() => {
    const state = resolveNetworkInterfaceMetrics({
      projectId,
      namespace,
      interfaces,
      probedNetworks,
      probeDenied,
      probeError,
      probeLoading,
    });
    const live = projectId && !state.unavailable && !state.isDenied;
    return {
      ...state,
      seriesLabels: workloadInterfaceSeriesLabels(instances),
      receiveTotalQuery: live ? networkTrafficTotalQuery(projectId, namespace, interfaces, 'receive') : undefined,
      transmitTotalQuery: live ? networkTrafficTotalQuery(projectId, namespace, interfaces, 'transmit') : undefined,
      throughputQuery: live ? networkThroughputQuery(projectId, namespace, interfaces) : undefined,
      refsByInstance: Object.fromEntries(
        instances.map((instance) => [instance.name, networkInterfaceRefs(instance.networkInterfaces)])
      ),
    };
  }, [projectId, namespace, interfaces, instances, probedNetworks, probeDenied, probeError, probeLoading]);
}
