// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// errNotInCell means the cell does not run the session's workload
// deployment, so the session is another cell's to claim.
var errNotInCell = errors.New("the session's instance does not run in this cell")

// rejection ends a session before it is claimed.
type rejection struct {
	reason  string
	message string
}

func reject(reason, format string, args ...any) *rejection {
	return &rejection{reason: reason, message: fmt.Sprintf(format, args...)}
}

// target is where a session's command runs.
type target struct {
	instance    *computev1alpha.Instance
	pod         *corev1.Pod
	container   string
	containerID string
	markerDir   string
}

// check runs the checks that come before a claim: the cell Instance, its pod,
// the container, and a shell and the requested executable in it.
func (a *Agent) check(ctx context.Context, s *computev1alpha.InstanceConsoleSession) (*target, *rejection, error) {
	inst, rej, err := a.findInstance(ctx, s)
	if rej != nil || err != nil {
		return nil, rej, err
	}
	pod, containerID, rej, err := a.findPod(ctx, inst, s.Spec.ContainerName)
	if rej != nil || err != nil {
		return nil, rej, err
	}
	dir, rej, err := a.probe(ctx, pod, s.Spec.ContainerName, s.Spec.Command[0])
	if rej != nil || err != nil {
		return nil, rej, err
	}
	return &target{
		instance:    inst,
		pod:         pod,
		container:   s.Spec.ContainerName,
		containerID: containerID,
		markerDir:   dir,
	}, nil, nil
}

// findInstance finds the cell Instance a session copy names. A same-name
// Instance from another workload deployment is a replacement, not the
// Instance the session was created for.
func (a *Agent) findInstance(ctx context.Context, s *computev1alpha.InstanceConsoleSession) (*computev1alpha.Instance, *rejection, error) {
	name := s.Labels[InstanceNameLabel]
	deploymentUID := s.Labels[computev1alpha.WorkloadDeploymentUIDLabel]
	if name == "" || deploymentUID == "" || len(s.Spec.Command) == 0 {
		return nil, reject(computev1alpha.InstanceConsoleSessionReasonInvalid,
			"The session was delivered without the instance it runs in."), nil
	}
	var inst computev1alpha.Instance
	err := a.sessions.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: name}, &inst)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, nil, err
	}
	if err == nil && inst.Labels[computev1alpha.WorkloadDeploymentUIDLabel] == deploymentUID {
		if !inst.DeletionTimestamp.IsZero() {
			return nil, reject(computev1alpha.InstanceConsoleSessionReasonInstanceNotRunning,
				"The instance is being deleted."), nil
		}
		return &inst, nil, nil
	}
	var siblings computev1alpha.InstanceList
	if err := a.sessions.List(ctx, &siblings, client.InNamespace(s.Namespace),
		client.MatchingLabels{computev1alpha.WorkloadDeploymentUIDLabel: deploymentUID}); err != nil {
		return nil, nil, err
	}
	if len(siblings.Items) == 0 {
		return nil, nil, errNotInCell
	}
	return nil, reject(computev1alpha.InstanceConsoleSessionReasonInstanceNotFound,
		"The instance no longer exists or has been replaced."), nil
}

// findPod finds the running pod that realizes a cell Instance, and the ID of
// the requested container in it.
func (a *Agent) findPod(ctx context.Context, inst *computev1alpha.Instance, container string) (*corev1.Pod, string, *rejection, error) {
	notRunning := reject(computev1alpha.InstanceConsoleSessionReasonInstanceNotRunning, "The instance is not running.")
	var pod corev1.Pod
	err := a.cell.Get(ctx, client.ObjectKeyFromObject(inst), &pod)
	if apierrors.IsNotFound(err) {
		return nil, "", notRunning, nil
	}
	if err != nil {
		return nil, "", nil, err
	}
	if owner := metav1.GetControllerOf(&pod); owner == nil || owner.UID != inst.UID {
		return nil, "", notRunning, nil
	}
	if !slices.Contains(a.cfg.ManagedBy, pod.Labels[managedByLabel]) {
		return nil, "", reject(computev1alpha.InstanceConsoleSessionReasonInvalid,
			"Sessions are not available for this instance's runtime."), nil
	}
	if !slices.ContainsFunc(pod.Spec.Containers, func(c corev1.Container) bool { return c.Name == container }) {
		return nil, "", reject(computev1alpha.InstanceConsoleSessionReasonInvalid,
			"The instance has no container named %q.", container), nil
	}
	if pod.Status.Phase != corev1.PodRunning || !pod.DeletionTimestamp.IsZero() {
		return nil, "", notRunning, nil
	}
	status := containerStatus(&pod, container)
	if status == nil || status.State.Running == nil {
		return nil, "", reject(computev1alpha.InstanceConsoleSessionReasonInstanceNotRunning,
			"The container %q is not running.", container), nil
	}
	return &pod, status.ContainerID, nil, nil
}

func containerStatus(pod *corev1.Pod, name string) *corev1.ContainerStatus {
	for i := range pod.Status.ContainerStatuses {
		if pod.Status.ContainerStatuses[i].Name == name {
			return &pod.Status.ContainerStatuses[i]
		}
	}
	return nil
}

type probeResult struct {
	mu          sync.Mutex
	dir         string
	executables map[string]bool
}

const maxProbeCacheEntries = 4096

// probe checks that a container has a shell, a writable directory for the
// marker file, and the requested executable. What it finds is cached per pod
// UID and container, so each check execs into an instance once.
func (a *Agent) probe(ctx context.Context, pod *corev1.Pod, container, executable string) (string, *rejection, error) {
	key := string(pod.UID) + "/" + container
	if v, ok := a.probes.Load(key); ok {
		r := v.(*probeResult)
		r.mu.Lock()
		dir, known := r.dir, r.executables[executable]
		r.mu.Unlock()
		if known {
			return dir, nil, nil
		}
	}

	out, code, err := run(ctx, a.exec, types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name},
		container, probeCommand(executable))
	var failed *ExecFailedError
	switch {
	case errors.As(err, &failed):
		return "", reject(computev1alpha.InstanceConsoleSessionReasonNoShell,
			"The container has no shell (sh), which the platform needs to manage the command."), nil
	case err != nil:
		return "", nil, err
	case code == probeExitCommandMissing:
		return "", reject(computev1alpha.InstanceConsoleSessionReasonCommandUnavailable,
			"The container has no executable named %q.", executable), nil
	case code == probeExitNoWritableDir:
		return "", reject(computev1alpha.InstanceConsoleSessionReasonNoShell,
			"The container has no writable temporary directory, which the platform needs to manage the command."), nil
	case code != 0:
		return "", reject(computev1alpha.InstanceConsoleSessionReasonNoShell,
			"The container's shell (sh) failed to run."), nil
	}

	lines := strings.Fields(out)
	if len(lines) == 0 {
		return "", reject(computev1alpha.InstanceConsoleSessionReasonNoShell,
			"The container's shell (sh) failed to run."), nil
	}
	dir := lines[len(lines)-1]
	if a.probeCacheSize() >= maxProbeCacheEntries {
		a.probes.Clear()
	}
	v, _ := a.probes.LoadOrStore(key, &probeResult{executables: map[string]bool{}})
	r := v.(*probeResult)
	r.mu.Lock()
	r.dir = dir
	r.executables[executable] = true
	r.mu.Unlock()
	return dir, nil, nil
}

func (a *Agent) probeCacheSize() int {
	n := 0
	a.probes.Range(func(_, _ any) bool {
		n++
		return true
	})
	return n
}
