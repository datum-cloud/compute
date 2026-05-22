// Package watch provides a rollout progress watcher for compute workloads.
package watch

import (
	"context"
	"fmt"
	"io"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
)

type deploymentPhase string

const (
	phasePending  deploymentPhase = "Pending"
	phaseUpdating deploymentPhase = "Updating"
	phaseDone     deploymentPhase = "Done"
	phaseBlocked  deploymentPhase = "Blocked"
)

type deploymentState struct {
	placement    string
	city         string
	desired      int32
	ready        int32
	current      int32
	phase        deploymentPhase
	stalledSince time.Time
}

// Rollout polls WorkloadDeployment objects for the given workload UID, printing
// per-city progress rows as state changes. It returns when all deployments
// reach Done, or when ctx is cancelled (Ctrl-C detach).
func Rollout(ctx context.Context, c client.Client, out io.Writer, project string, workloadUID types.UID) error {
	start := time.Now()

	selector := labels.SelectorFromSet(labels.Set{
		computev1alpha.WorkloadUIDLabel: string(workloadUID),
	})

	tw := util.NewTabWriter(out)
	headerPrinted := false

	states := map[string]*deploymentState{}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			tw.Flush()
			fmt.Fprintln(out, "Detached. Rollout continues in background.")
			return nil

		case <-ticker.C:
			var deployList computev1alpha.WorkloadDeploymentList
			if err := c.List(ctx, &deployList,
				client.InNamespace(util.ResourceNamespace),
				client.MatchingLabelsSelector{Selector: selector},
			); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				// Transient error — keep polling.
				continue
			}

			if len(deployList.Items) == 0 {
				continue
			}

			if !headerPrinted {
				fmt.Fprintln(out, "\n  PLACEMENT\tCITY\tUPDATED\tREADY\tOLD\tPHASE")
				headerPrinted = true
			}

			allDone := true
			for _, d := range deployList.Items {
				key := d.Spec.CityCode
				prev, exists := states[key]

				desired := d.Status.DesiredReplicas
				ready := d.Status.ReadyReplicas
				current := d.Status.CurrentReplicas

				newPhase := computePhase(desired, ready, current, prev)

				if !exists || prev.desired != desired || prev.ready != ready || prev.current != current || prev.phase != newPhase {
					st := &deploymentState{
						placement: d.Spec.PlacementName,
						city:      d.Spec.CityCode,
						desired:   desired,
						ready:     ready,
						current:   current,
						phase:     newPhase,
					}
					if exists {
						st.stalledSince = prev.stalledSince
					}

					// Track when we first noticed a potential stall.
					if newPhase != phaseDone && newPhase != phasePending {
						if !exists || prev.phase == phasePending {
							st.stalledSince = time.Now()
						} else if exists && prev.ready == ready && prev.current == current {
							st.stalledSince = prev.stalledSince
						} else {
							st.stalledSince = time.Now()
						}
					}

					// Promote to Blocked if stalled > 30s without progress.
					if newPhase == phaseUpdating && !st.stalledSince.IsZero() && time.Since(st.stalledSince) > 30*time.Second {
						st.phase = phaseBlocked
						newPhase = phaseBlocked
					}

					states[key] = st

					// Compute old replicas = total created minus current-template replicas.
					old := d.Status.Replicas - d.Status.CurrentReplicas
					if old < 0 {
						old = 0
					}

					fmt.Fprintf(tw, "  %s\t%s\t%d\t%d\t%d\t%s\n",
						d.Spec.PlacementName,
						d.Spec.CityCode,
						current,
						ready,
						old,
						string(newPhase),
					)
					tw.Flush()

					if newPhase == phaseBlocked {
						printBlockedDetail(ctx, c, out, project, d)
					}
				}

				if newPhase != phaseDone {
					allDone = false
				}
			}

			if allDone && len(deployList.Items) > 0 {
				elapsed := time.Since(start).Round(time.Second)
				minutes := int(elapsed.Minutes())
				seconds := int(elapsed.Seconds()) % 60
				if minutes > 0 {
					fmt.Fprintf(out, "Rollout complete in %dm %ds.\n", minutes, seconds)
				} else {
					fmt.Fprintf(out, "Rollout complete in %ds.\n", seconds)
				}
				return nil
			}
		}
	}
}

func computePhase(desired, ready, current int32, prev *deploymentState) deploymentPhase {
	if desired == 0 {
		return phaseDone
	}
	if current == 0 {
		return phasePending
	}
	if ready >= desired && current >= desired {
		return phaseDone
	}
	return phaseUpdating
}

// printBlockedDetail fetches instances for the deployment and prints a reason
// for the first non-ready instance.
func printBlockedDetail(ctx context.Context, c client.Client, out io.Writer, project string, d computev1alpha.WorkloadDeployment) {
	selector := labels.SelectorFromSet(labels.Set{
		computev1alpha.WorkloadDeploymentUIDLabel: string(d.UID),
	})
	var instList computev1alpha.InstanceList
	if err := c.List(ctx, &instList, client.InNamespace(util.ResourceNamespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return
	}
	for _, inst := range instList.Items {
		ready := util.FindCondition(inst.Status.Conditions, computev1alpha.InstanceReady)
		if ready == nil || ready.Status != "True" {
			status, detail := util.InstanceStatusDetail(inst.Status.Conditions)
			if detail != "" {
				fmt.Fprintf(out, "    Blocked reason: %s — %s\n", status, detail)
			} else {
				fmt.Fprintf(out, "    Blocked reason: %s\n", status)
			}
			return
		}
	}
}
