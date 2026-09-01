package agent

import (
	"fmt"
	"sort"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Level is where in the resource tree a condition was found.
type Level string

const (
	LevelWorkload   Level = "workload"
	LevelDeployment Level = "deployment"
	LevelInstance   Level = "instance"
)

// depth orders the levels: a cause found deeper in the tree is more specific
// than a shallower one and wins.
var depth = map[Level]int{
	LevelWorkload:   0,
	LevelDeployment: 1,
	LevelInstance:   2,
}

// Cause is one failing condition, joined to its catalog entry.
type Cause struct {
	Level              Level         `json:"level"`
	Object             string        `json:"object"`
	ConditionType      string        `json:"conditionType"`
	Status             string        `json:"status"`
	Reason             string        `json:"reason"`
	Message            string        `json:"message"`
	LastTransitionTime metav1.Time   `json:"lastTransitionTime"`
	Actionability      Actionability `json:"actionability,omitempty"`
	Explanation        string        `json:"explanation"`
	Remediation        string        `json:"remediation,omitempty"`
	Skill              string        `json:"skill,omitempty"`
}

// BlockedInstance names an instance that is not Ready, with its own most
// specific failing reason.
type BlockedInstance struct {
	Name    string `json:"name"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

// InstanceSummary reports how a failure is distributed across replicas.
type InstanceSummary struct {
	Total   int               `json:"total"`
	Ready   int               `json:"ready"`
	Blocked []BlockedInstance `json:"blocked"`
}

// Diagnosis is the answer: one root cause, the evidence behind it, and what to
// do next.
type Diagnosis struct {
	Workload  string `json:"workload"`
	Namespace string `json:"namespace"`
	Available bool   `json:"available"`
	Summary   string `json:"summary"`
	RootCause *Cause `json:"rootCause"`
	// ContributingConditions is every failing condition found, ranked the same
	// way the root cause was chosen — the evidence trail behind the answer.
	ContributingConditions []Cause         `json:"contributingConditions"`
	Instances              InstanceSummary `json:"instances"`
	NextSteps              []string        `json:"nextSteps"`
	SuggestedSkill         string          `json:"suggestedSkill,omitempty"`
}

// Diagnose explains why a Workload is not available.
//
// It walks Workload -> WorkloadDeployment -> Instance and returns the deepest
// condition that actually names a cause. This matters because compute's
// top-level reasons are deliberately pointers: Workload.Available=False with
// reason QuotaNotGranted says which subsystem is blocking, while the real
// reason lives on an Instance's QuotaGranted condition below it. Reporting the
// pointer is a wrong answer that reads like a right one.
//
// The caller supplies the objects; this function performs no I/O, so the
// ranking is testable without a cluster.
func Diagnose(
	workload *computev1alpha.Workload,
	deployments []computev1alpha.WorkloadDeployment,
	instances []computev1alpha.Instance,
) Diagnosis {
	var causes []Cause

	for _, c := range failing(workload.Status.Conditions) {
		causes = append(causes, toCause(LevelWorkload, workload.Name, c))
	}
	for i := range deployments {
		d := &deployments[i]
		for _, c := range failing(d.Status.Conditions) {
			causes = append(causes, toCause(LevelDeployment, d.Name, c))
		}
	}
	for i := range instances {
		inst := &instances[i]
		for _, c := range failing(inst.Status.Conditions) {
			causes = append(causes, toCause(LevelInstance, inst.Name, c))
		}
	}

	// Prefer a condition that names a real cause over one that only points at
	// another condition; among equals, prefer the deepest. SliceStable keeps
	// discovery order for otherwise-equal causes so output is deterministic.
	sort.SliceStable(causes, func(i, j int) bool {
		pi, pj := IsPointerReason(causes[i].Reason), IsPointerReason(causes[j].Reason)
		if pi != pj {
			return !pi
		}
		return depth[causes[i].Level] > depth[causes[j].Level]
	})

	var rootCause *Cause
	if len(causes) > 0 {
		rootCause = &causes[0]
	}

	summary := instanceSummary(instances)
	available := apimeta.IsStatusConditionTrue(workload.Status.Conditions, computev1alpha.WorkloadAvailable)

	d := Diagnosis{
		Workload:               workload.Name,
		Namespace:              workload.Namespace,
		Available:              available,
		RootCause:              rootCause,
		ContributingConditions: causes,
		Instances:              summary,
		NextSteps:              nextSteps(rootCause),
	}
	d.Summary = summarize(workload, available, rootCause, len(summary.Blocked))
	if rootCause != nil {
		d.SuggestedSkill = rootCause.Skill
	}
	return d
}

// failing returns the conditions that are not True. Every condition compute
// sets is positive-polarity, so "not True" is the same as "blocking".
func failing(conditions []metav1.Condition) []metav1.Condition {
	var out []metav1.Condition
	for _, c := range conditions {
		if c.Status != metav1.ConditionTrue {
			out = append(out, c)
		}
	}
	return out
}

func toCause(level Level, object string, c metav1.Condition) Cause {
	cause := Cause{
		Level:              level,
		Object:             object,
		ConditionType:      c.Type,
		Status:             string(c.Status),
		Reason:             c.Reason,
		Message:            c.Message,
		LastTransitionTime: c.LastTransitionTime,
	}
	if info, ok := ExplainReason(c.Reason); ok {
		cause.Actionability = info.Actionability
		cause.Explanation = info.Explanation
		cause.Remediation = info.Remediation
		cause.Skill = info.Skill
	} else {
		// An uncatalogued reason is still worth reporting — the condition
		// message is the best available cause — but say the classification is
		// unknown rather than implying one.
		cause.Explanation = fmt.Sprintf(
			"No catalog entry for reason %q. Treat the condition message as the cause.", c.Reason)
	}
	return cause
}

func instanceSummary(instances []computev1alpha.Instance) InstanceSummary {
	s := InstanceSummary{Total: len(instances)}
	for i := range instances {
		inst := &instances[i]
		if apimeta.IsStatusConditionTrue(inst.Status.Conditions, computev1alpha.InstanceReady) {
			s.Ready++
			continue
		}
		blocked := BlockedInstance{Name: inst.Name, Reason: "Unknown"}
		// Report the instance's own most specific failing condition, skipping
		// the ones that merely point elsewhere.
		f := failing(inst.Status.Conditions)
		var chosen *metav1.Condition
		for i := range f {
			if !IsPointerReason(f[i].Reason) {
				chosen = &f[i]
				break
			}
		}
		if chosen == nil && len(f) > 0 {
			chosen = &f[0]
		}
		if chosen != nil {
			blocked.Reason = chosen.Reason
			blocked.Message = chosen.Message
		}
		s.Blocked = append(s.Blocked, blocked)
	}
	return s
}

func summarize(w *computev1alpha.Workload, available bool, root *Cause, blockedCount int) string {
	if available && blockedCount == 0 {
		return fmt.Sprintf("Workload %s is available: %d/%d replicas ready.",
			w.Name, w.Status.ReadyReplicas, w.Status.DesiredReplicas)
	}
	if root == nil {
		return fmt.Sprintf(
			"Workload %s reports no failing conditions but only %d/%d replicas are ready.",
			w.Name, w.Status.ReadyReplicas, w.Status.DesiredReplicas)
	}

	var who string
	switch root.Actionability {
	case ActionabilityUser:
		who = "This is user-actionable"
	case ActionabilityPlatform:
		who = "This is a platform fault — escalate to Datum"
	case ActionabilityTransient:
		who = "This is a transient state"
	default:
		who = "The owner of this cause is unclassified"
	}

	state := "not available"
	if available {
		state = "partially available"
	}
	return fmt.Sprintf(
		"Workload %s is %s (%d/%d replicas ready). Root cause: %s on %s %s (%s). %s.",
		w.Name, state, w.Status.ReadyReplicas, w.Status.DesiredReplicas,
		root.Reason, root.Level, root.Object, root.ConditionType, who)
}

func nextSteps(root *Cause) []string {
	if root == nil {
		return nil
	}
	var steps []string
	if root.Remediation != "" {
		steps = append(steps, root.Remediation)
	}
	if root.Actionability == ActionabilityPlatform {
		steps = append(steps,
			"Do not change the workload spec — this cause is not fixable from the customer side.")
	}
	if root.Skill != "" {
		steps = append(steps, fmt.Sprintf("Load the %q skill for the full procedure.", root.Skill))
	}
	return steps
}
