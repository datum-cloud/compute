/**
 * Zod schemas for the provider plugin's Workload detail view — adapted from
 * `ui/consumer/src/schema.ts` (same underlying compute API), extended with a
 * couple of fields the consumer dashboard doesn't need but a support view
 * does: per-instance scheduling gates and the suspended flag (both are on
 * `api/v1alpha/instance_types.go`'s `InstanceSpec.Controller` /
 * `InstanceStatus.Suspended`).
 */
import { z } from 'zod';

// ── Workload ─────────────────────────────────────────────────────────────

export type WorkloadHealth = 'Available' | 'Degraded' | 'Unavailable' | 'Unknown';

export const conditionSchema = z.object({
  type: z.string(),
  status: z.enum(['True', 'False', 'Unknown']),
  reason: z.string().optional(),
  message: z.string().optional(),
  lastTransitionTime: z.string().optional(),
  observedGeneration: z.number().optional(),
});

export type Condition = z.infer<typeof conditionSchema>;

export const workloadPlacementSchema = z.object({
  name: z.string(),
  cityCodes: z.array(z.string()).default([]),
  readyReplicas: z.number(),
  desiredReplicas: z.number(),
  currentReplicas: z.number(),
  health: z.enum(['Available', 'Degraded', 'Unavailable', 'Unknown']),
  conditions: z.array(conditionSchema).default([]),
});

export type WorkloadPlacement = z.infer<typeof workloadPlacementSchema>;

export const workloadResourceSchema = z.object({
  uid: z.string(),
  name: z.string(),
  namespace: z.string().optional(),
  createdAt: z.coerce.date(),
  image: z.string().optional(),
  health: z.enum(['Available', 'Degraded', 'Unavailable', 'Unknown']),
  currentReplicas: z.number(),
  updatedReplicas: z.number(),
  readyReplicas: z.number(),
  desiredReplicas: z.number(),
  placements: z.array(workloadPlacementSchema).default([]),
  conditions: z.array(conditionSchema).default([]),
  runtimeType: z.string().optional(),
  regions: z.array(z.string()).default([]),
  resources: z.string().optional(),
  replicasPerRegion: z.number().optional(),
});

export type Workload = z.infer<typeof workloadResourceSchema>;

export const workloadListSchema = z.object({
  items: z.array(workloadResourceSchema),
});

export type WorkloadList = z.infer<typeof workloadListSchema>;

/** Maps a health/condition-status value to a `Badge` `type` prop. */
export function healthToBadgeType(
  health: WorkloadHealth | 'True' | 'False'
): 'success' | 'warning' | 'danger' | 'muted' {
  switch (health) {
    case 'Available':
    case 'True':
      return 'success';
    case 'Degraded':
      return 'warning';
    case 'Unavailable':
    case 'False':
      return 'danger';
    default:
      return 'muted';
  }
}

// ── Instance ─────────────────────────────────────────────────────────────

export type InstanceStatusValue = 'Available' | 'Pending' | 'Failed' | 'Unknown';

export const instanceResourceSchema = z.object({
  uid: z.string(),
  name: z.string(),
  namespace: z.string().optional(),
  createdAt: z.coerce.date(),
  city: z.string().optional(),
  placement: z.string().optional(),
  instanceType: z.string().optional(),
  cpu: z.string().optional(),
  memory: z.string().optional(),
  image: z.string().optional(),
  status: z.enum(['Available', 'Pending', 'Failed', 'Unknown']),
  externalIP: z.string().optional(),
  internalIP: z.string().optional(),
  conditions: z.array(conditionSchema).default([]),
  /** Present while the instance is gated from scheduling (e.g. awaiting quota). */
  schedulingGates: z.array(z.string()).default([]),
  suspended: z.boolean().default(false),
});

export type Instance = z.infer<typeof instanceResourceSchema>;

export const instanceListSchema = z.object({
  items: z.array(instanceResourceSchema),
});

export type InstanceList = z.infer<typeof instanceListSchema>;

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
