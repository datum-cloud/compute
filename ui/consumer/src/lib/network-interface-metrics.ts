/**
 * Network in/out charts on the instance Metrics tab. A label-values probe on
 * the `network` label decides whether the VPC interface traffic series exist
 * for this instance's interface refs before the charts query them.
 */
import { useMemo } from 'react';
import {
  networkInterfaceRefs,
  networkInterfaceSeriesLabels,
  networkTrafficQuery,
  networkTrafficSeriesMatch,
} from './metrics-queries';
import { isPrometheusDenied, usePrometheusLabelValues } from './prometheus';
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
