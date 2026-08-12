/**
 * Zod schemas for the compute plugin's two resources — pure data shapes,
 * ported near-verbatim from cloud-portal PR #1315's
 * `app/resources/workloads/workload.schema.ts` and
 * `app/resources/instances/instance.schema.ts`. Combined into one file here
 * since the plugin only exposes list/detail read views for both resources.
 */
import { z } from 'zod';

// ── Workload ─────────────────────────────────────────────────────────────

export type WorkloadHealth = 'Available' | 'Degraded' | 'Unavailable' | 'Unknown';

const workloadConditionSchema = z.object({
  type: z.string(),
  status: z.enum(['True', 'False', 'Unknown']),
  reason: z.string().optional(),
  message: z.string().optional(),
  lastTransitionTime: z.string().optional(),
  observedGeneration: z.number().optional(),
});

export const workloadPlacementRegionSchema = z.object({
  name: z.string(),
  cityCodes: z.array(z.string()).default([]),
  readyReplicas: z.number(),
  desiredReplicas: z.number(),
  health: z.enum(['Available', 'Degraded', 'Unavailable', 'Unknown']),
});

export type WorkloadPlacementRegion = z.infer<typeof workloadPlacementRegionSchema>;

export const workloadResourceSchema = z.object({
  uid: z.string(),
  name: z.string(),
  namespace: z.string().optional(),
  resourceVersion: z.string().optional(),
  createdAt: z.coerce.date(),
  /** Latest status condition transition, when present — used for “Updated … ago”. */
  updatedAt: z.coerce.date().optional(),
  image: z.string().optional(),
  health: z.enum(['Available', 'Degraded', 'Unavailable', 'Unknown']),
  /** Programmed replicas (latest template applied) — not the same as ready. */
  currentReplicas: z.number(),
  /** Ready-to-serve replicas — prefer this for health counts. */
  readyReplicas: z.number(),
  desiredReplicas: z.number(),
  /** Placement names (legacy list consumers). */
  placements: z.array(z.string()),
  /** Spec + status joined for region rows on the home cards. */
  placementRegions: z.array(workloadPlacementRegionSchema).default([]),
  conditions: z.array(workloadConditionSchema).default([]),
  // Detail/overview fields
  runtimeType: z.string().optional(),
  tags: z.array(z.string()).default([]),
  ports: z.array(z.string()).default([]),
  regions: z.array(z.string()).default([]),
  resources: z.string().optional(),
  replicasPerRegion: z.number().optional(),
});

export type Workload = z.infer<typeof workloadResourceSchema>;

export const workloadListSchema = z.object({
  items: z.array(workloadResourceSchema),
});

export type WorkloadList = z.infer<typeof workloadListSchema>;

/**
 * Maps a workload health value to a `Badge` `type` prop understood by
 * `@datum-cloud/datum-ui/badge`. Shared by the list and detail pages.
 */
export function workloadHealthToBadgeType(
  health: WorkloadHealth
): 'success' | 'warning' | 'danger' | 'muted' {
  switch (health) {
    case 'Available':
      return 'success';
    case 'Degraded':
      return 'warning';
    case 'Unavailable':
      return 'danger';
    default:
      return 'muted';
  }
}

// ── Instance ─────────────────────────────────────────────────────────────

export type InstanceStatusValue = 'Available' | 'Pending' | 'Failed' | 'Unknown';

const instanceConditionSchema = z.object({
  type: z.string(),
  status: z.enum(['True', 'False', 'Unknown']),
  reason: z.string().optional(),
  message: z.string().optional(),
  lastTransitionTime: z.string().optional(),
});

export type InstanceCondition = z.infer<typeof instanceConditionSchema>;

export const instanceResourceSchema = z.object({
  uid: z.string(),
  name: z.string(),
  namespace: z.string().optional(),
  createdAt: z.coerce.date(),
  workloadName: z.string().optional(),
  workloadUid: z.string().optional(),
  city: z.string().optional(),
  placement: z.string().optional(),
  instanceType: z.string().optional(),
  /** Allocated CPU — from requests, or resolved from instanceType catalog (e.g. "1"). */
  cpu: z.string().optional(),
  /** Allocated memory — from requests, or resolved from instanceType catalog (e.g. "2Gi"). */
  memory: z.string().optional(),
  image: z.string().optional(),
  ports: z.array(z.string()).default([]),
  status: z.enum(['Available', 'Pending', 'Failed', 'Unknown']),
  externalIP: z.string().optional(),
  internalIP: z.string().optional(),
  conditions: z.array(instanceConditionSchema).default([]),
});

export type Instance = z.infer<typeof instanceResourceSchema>;

export const instanceListSchema = z.object({
  items: z.array(instanceResourceSchema),
});

export type InstanceList = z.infer<typeof instanceListSchema>;

/**
 * Maps an instance status value to a `Badge` `type` prop understood by
 * `@datum-cloud/datum-ui/badge`.
 */
export function instanceStatusToBadgeType(
  status: InstanceStatusValue
): 'success' | 'warning' | 'danger' | 'muted' {
  switch (status) {
    case 'Available':
      return 'success';
    case 'Pending':
      return 'warning';
    case 'Failed':
      return 'danger';
    default:
      return 'muted';
  }
}
