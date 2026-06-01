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
			_ = tw.Flush()
			_, _ = fmt.Fprintln(out, "Detached. Rollout continues in background.")
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
				_, _ = fmt.Fprintln(out, "\n  PLACEMENT\tCITY\tUPDATED\tREADY\tOLD\tPHASE")
				headerPrinted = true
			}

			allDone := processDeployments(ctx, c, out, project, tw, states, deployList.Items)

			if allDone && len(deployList.Items) > 0 {
				printElapsed(out, time.Since(start).Round(time.Second))
				return nil
			}
		}
	}
}

// tabFlusher is a writer that can also be flushed (e.g. tabwriter.Writer).
type tabFlusher interface {
	io.Writer
	Flush() error
}

// processDeployments updates state for each deployment, prints changed rows,
// and returns true when every deployment has reached the Done phase.
func processDeployments(
	ctx context.Context,
	c client.Client,
	out io.Writer,
	project string,
	tw tabFlusher,
	states map[string]*deploymentState,
	deployments []computev1alpha.WorkloadDeployment,
) bool {
	allDone := true
	for _, d := range deployments {
		key := d.Spec.CityCode
		prev, exists := states[key]

		desired := resolveDesired(d)
		ready := d.Status.ReadyReplicas
		current := d.Status.CurrentReplicas

		newPhase := computePhase(desired, ready, current, d.Status.Replicas)

		if !exists || prev.desired != desired || prev.ready != ready || prev.current != current || prev.phase != newPhase {
			newPhase = updateDeploymentState(states, key, d, exists, prev, desired, ready, current, newPhase)
			printDeploymentRow(ctx, c, out, project, tw, d, current, ready, newPhase)
		}

		if newPhase != phaseDone {
			allDone = false
		}
	}
	return allDone
}

// updateDeploymentState updates the states map for a deployment and returns the
// (possibly promoted) phase.
func updateDeploymentState(
	states map[string]*deploymentState,
	key string,
	d computev1alpha.WorkloadDeployment,
	exists bool,
	prev *deploymentState,
	desired, ready, current int32,
	newPhase deploymentPhase,
) deploymentPhase {
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
		} else if prev.ready == ready && prev.current == current {
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
	return newPhase
}

// printDeploymentRow writes a progress row and, if blocked, detail about the
// first non-ready instance.
func printDeploymentRow(
	ctx context.Context,
	c client.Client,
	out io.Writer,
	project string,
	tw tabFlusher,
	d computev1alpha.WorkloadDeployment,
	current, ready int32,
	newPhase deploymentPhase,
) {
	old := max(d.Status.Replicas-d.Status.CurrentReplicas, 0)

	_, _ = fmt.Fprintf(tw, "  %s\t%s\t%d\t%d\t%d\t%s\n",
		d.Spec.PlacementName,
		d.Spec.CityCode,
		current,
		ready,
		old,
		string(newPhase),
	)
	_ = tw.Flush()

	if newPhase == phaseBlocked {
		printBlockedDetail(ctx, c, out, project, d)
	}
}

// printElapsed writes the total rollout duration to out.
func printElapsed(out io.Writer, elapsed time.Duration) {
	minutes := int(elapsed.Minutes())
	seconds := int(elapsed.Seconds()) % 60
	if minutes > 0 {
		_, _ = fmt.Fprintf(out, "Rollout complete in %dm %ds.\n", minutes, seconds)
	} else {
		_, _ = fmt.Fprintf(out, "Rollout complete in %ds.\n", seconds)
	}
}

// resolveDesired returns the replica count the rollout should wait for.
// Status.DesiredReplicas stays at zero until the controller first reconciles
// the deployment; until then, fall back to the spec minimum so a freshly
// created deployment isn't reported Done before any instances are scheduled.
// Once the controller has reported a desired count, trust it.
func resolveDesired(d computev1alpha.WorkloadDeployment) int32 {
	if d.Status.DesiredReplicas == 0 {
		return d.Spec.ScaleSettings.MinReplicas
	}
	return d.Status.DesiredReplicas
}

func computePhase(desired, ready, current, replicas int32) deploymentPhase {
	if desired == 0 {
		return phaseDone
	}
	if ready >= desired && current >= desired && replicas <= current {
		return phaseDone
	}
	if current == 0 {
		return phasePending
	}
	return phaseUpdating
}

// printBlockedDetail fetches instances for the deployment and prints a reason
// for the first non-ready instance.
func printBlockedDetail(ctx context.Context, c client.Client, out io.Writer, _ string, d computev1alpha.WorkloadDeployment) {
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
