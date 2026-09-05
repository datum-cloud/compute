package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"

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
	Level         Level  `json:"level"`
	Object        string `json:"object"`
	ConditionType string `json:"conditionType"`
	Status        string `json:"status"`
	Reason        string `json:"reason"`
	Message       string `json:"message"`
	// LastTransitionTime is RFC 3339. It is a string rather than a metav1.Time
	// because this struct is a tool's output schema: metav1.Time marshals as a
	// string but is a struct, which JSON Schema inference rejects.
	LastTransitionTime string `json:"lastTransitionTime,omitempty"`
	// InStateFor is how long the condition has held this status, rendered for
	// a reader ("5d", "12m"). Derived from LastTransitionTime against the
	// clock at read time, never stored; empty when the API left it unset or
	// set a value that cannot be believed.
	InStateFor string `json:"inStateFor,omitempty"`
	// Reported separates a condition status of False — something looked and
	// reported a failure — from Unknown, where nothing has reported success or
	// failure and the reason states an intent rather than an observation.
	// Flattening the two turns "we have heard nothing" into "we were told it is
	// in progress", which is the more confident and the more wrong answer.
	Reported bool `json:"reported"`
	// ObjectAge is how long the object carrying this condition has existed,
	// from its creationTimestamp.
	ObjectAge string `json:"objectAge,omitempty"`
	// FailingFor is a floor on how long this object has been failing to reach
	// its ready state: the greater of InStateFor and ObjectAge, set only while
	// the object is not ready. It is deliberately NOT a claim that Reason held
	// for that whole span — a crash rewrites lastTransitionTime, so InStateFor
	// measures the last rewrite, not the outage. Read InStateFor as "this
	// reason has held this long" and FailingFor as "this object has been broken
	// at least this long"; the gap between them is itself the signal.
	FailingFor string `json:"failingFor,omitempty"`
	// Pattern names a failure shape that no controller reported and only the
	// elapsed evidence reveals. Empty unless one was recognised.
	Pattern       string        `json:"pattern,omitempty"`
	Actionability Actionability `json:"actionability,omitempty"`
	Explanation   string        `json:"explanation"`
	Remediation   string        `json:"remediation,omitempty"`
	Skill         string        `json:"skill,omitempty"`
}

// PatternNoTerminalStateReported is the shape the staging stall took: an object
// that has outlived its reason's window without ever reaching ready, whose
// condition status is still Unknown, so nothing has reported success or
// failure. Something is acting on the object and reporting nothing either way.
//
// It is named for exactly what is observed. It is not a claim about who is at
// fault: infrastructure that never reports back and a container that starts and
// exits immediately look identical from inside the customer's project.
const PatternNoTerminalStateReported = "NoTerminalStateReported"

// note appends a derived observation to an explanation, so what the catalog
// says and what the clock says arrive in the same field.
func (c *Cause) note(s string) {
	if s == "" {
		return
	}
	c.Explanation = strings.TrimSpace(c.Explanation + " " + s)
}

// object is the context a condition is read in: which object carried it, when
// that object came into existence, and whether the object is reaching its own
// ready state. The last two are what let a cause report how long the object has
// been broken rather than only how long the condition has held.
type object struct {
	level    Level
	name     string
	created  metav1.Time
	notReady bool
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
	return DiagnoseAt(time.Now(), workload, deployments, instances)
}

// DiagnoseAt is Diagnose with the clock supplied. How long a condition has held
// is part of the answer, so the clock is an input rather than something the
// walk reaches for, and a test can pin an age instead of racing wall time.
func DiagnoseAt(
	now time.Time,
	workload *computev1alpha.Workload,
	deployments []computev1alpha.WorkloadDeployment,
	instances []computev1alpha.Instance,
) Diagnosis {
	var causes []Cause

	available := apimeta.IsStatusConditionTrue(workload.Status.Conditions, computev1alpha.WorkloadAvailable)

	wObj := object{
		level: LevelWorkload, name: workload.Name,
		created: workload.CreationTimestamp, notReady: !available,
	}
	for _, c := range failing(workload.Status.Conditions) {
		causes = append(causes, toCause(wObj, c, now))
	}
	for i := range deployments {
		d := &deployments[i]
		dObj := object{
			level: LevelDeployment, name: d.Name, created: d.CreationTimestamp,
			notReady: !apimeta.IsStatusConditionTrue(d.Status.Conditions, computev1alpha.WorkloadDeploymentAvailable),
		}
		for _, c := range failing(d.Status.Conditions) {
			causes = append(causes, toCause(dObj, c, now))
		}
	}
	for i := range instances {
		inst := &instances[i]
		iObj := object{
			level: LevelInstance, name: inst.Name, created: inst.CreationTimestamp,
			notReady: !apimeta.IsStatusConditionTrue(inst.Status.Conditions, computev1alpha.InstanceReady),
		}
		for _, c := range failing(inst.Status.Conditions) {
			causes = append(causes, toCause(iObj, c, now))
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

// minPlausibleTime is the floor a timestamp must clear to count as an
// observation.
//
// Real Instances carry lastTransitionTime "1970-01-01T00:00:00Z" as an unset
// sentinel, and metav1.Time.IsZero() does not catch it because Go's zero time
// is year 1, not 1970. The epoch therefore sails through and renders as an age
// of twenty thousand days, which is enough on its own to call a healthy object
// stalled. Nothing in compute predates its own API by decades, so a timestamp
// at or below this floor is a sentinel, not a fact.
var minPlausibleTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// plausible reports whether a timestamp can be believed as a real observation.
func plausible(t metav1.Time) bool {
	return !t.IsZero() && t.After(minPlausibleTime)
}

// formatTime renders a timestamp as RFC 3339, or empty when it is unset or
// implausible. An implausible value is dropped rather than passed on: the
// consumer is a language model that will do arithmetic on whatever it is given.
func formatTime(t metav1.Time) string {
	if !plausible(t) {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// transitionTime renders a condition's lastTransitionTime, discarding it when
// it cannot be believed. Beyond the absolute floor, a condition cannot have
// transitioned before the object carrying it existed, so anything older than
// creationTimestamp is a sentinel too.
func transitionTime(c metav1.Condition, created metav1.Time) string {
	if plausible(created) && c.LastTransitionTime.Time.Before(created.Time) {
		return ""
	}
	return formatTime(c.LastTransitionTime)
}

// discardedTime describes a timestamp that was thrown away, if one was. Saying
// so is part of the evidence: a reader who sees no age should know whether the
// API set none or set one that could not be believed.
func discardedTime(c metav1.Condition, kept string) string {
	if kept != "" || c.LastTransitionTime.IsZero() {
		return ""
	}
	return fmt.Sprintf(
		"The time recorded against this status (%s) is a placeholder Datum writes when it has "+
			"nothing real to record, so nothing was worked out from it about how long this has "+
			"been going on.",
		c.LastTransitionTime.UTC().Format(time.RFC3339))
}

// age returns how long ago an RFC 3339 timestamp was, relative to now.
//
// It refuses an empty, unparseable, implausible, or future value rather than
// guessing one. Everything downstream reads age as evidence about whether a
// state is stuck, and a fabricated age is worse than none.
func age(rfc3339 string, now time.Time) (time.Duration, bool) {
	if rfc3339 == "" {
		return 0, false
	}
	t, err := time.Parse(time.RFC3339, rfc3339)
	if err != nil {
		return 0, false
	}
	if !t.After(minPlausibleTime) {
		return 0, false
	}
	d := now.Sub(t)
	if d < 0 {
		return 0, false
	}
	return d, true
}

// humanDuration renders a duration coarsest-unit-first, the way an operator
// would say it. The consumer is a language model reading a tool result: "5d"
// lands where a nanosecond count, or a pair of timestamps it has to subtract,
// does not.
func humanDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return ""
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		if m := int(d.Minutes()) % 60; m > 0 {
			return fmt.Sprintf("%dh%dm", int(d.Hours()), m)
		}
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		days, hours := int(d.Hours())/24, int(d.Hours())%24
		if hours > 0 {
			return fmt.Sprintf("%dd%dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	}
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

func toCause(obj object, c metav1.Condition, now time.Time) Cause {
	cause := Cause{
		Level:              obj.level,
		Object:             obj.name,
		ConditionType:      c.Type,
		Status:             string(c.Status),
		Reported:           c.Status == metav1.ConditionFalse,
		Reason:             c.Reason,
		Message:            c.Message,
		LastTransitionTime: transitionTime(c, obj.created),
	}

	var inState time.Duration
	if elapsed, ok := age(cause.LastTransitionTime, now); ok {
		inState = elapsed
		cause.InStateFor = humanDuration(elapsed)
	}

	// creationTimestamp is the floor on how long the object has been broken:
	// every crash and restart rewrites lastTransitionTime, so the condition
	// clock measures churn, and it understates most in the crash-loop case that
	// matters most. The object clock cannot be reset that way.
	var objectAge time.Duration
	if elapsed, ok := age(formatTime(obj.created), now); ok {
		objectAge = elapsed
		cause.ObjectAge = humanDuration(elapsed)
	}
	if obj.notReady {
		cause.FailingFor = humanDuration(max(inState, objectAge))
	}

	info, ok := ExplainReason(c.Reason)
	if !ok {
		// An uncatalogued reason is still worth reporting — the condition
		// message is the best available cause — but say the classification is
		// unknown rather than implying one.
		cause.Explanation = fmt.Sprintf(
			"Datum reported %q, which this diagnosis has no explanation written for. "+
				"Go by the status message: it is the best account of what happened.", c.Reason)
		cause.note(discardedTime(c, cause.LastTransitionTime))
		return cause
	}

	cause.Actionability = ActionabilityAt(info, cause.LastTransitionTime, now)
	cause.Explanation = info.Explanation
	if !cause.Reported {
		// The catalogued explanations are written as observations ("the
		// provider is actively programming the instance"). Under Unknown
		// nothing observed that, so it is framed as the claim it is rather
		// than asserted and then contradicted two sentences later.
		cause.Explanation = "Nobody has confirmed this — it is what the status claims, not what was " +
			"seen: " + info.Explanation
	}
	cause.Remediation = info.Remediation
	cause.Skill = info.Skill
	if cause.Actionability == ActionabilityStalled {
		// The catalogued remediation for these reasons is "wait", which is
		// now the wrong advice: it is what produced the five-day wait this
		// escalation exists to end.
		cause.Remediation = fmt.Sprintf(
			"Stop waiting on this one. A step like this normally finishes within %s, and this one "+
				"has been sitting for %s. Take it to Datum, with the name (%s), the status code "+
				"(%s), and how long it has been stuck.",
			info.ExpectedWithin, cause.InStateFor, cause.Object, c.Reason)
	}
	switch {
	case noTerminalStateReported(obj, cause, info, objectAge, inState):
		applyNoTerminalState(&cause, info)
	case !cause.Reported:
		// The catalogued explanations are written as observations ("the
		// provider is actively programming the instance"). Under Unknown
		// nothing observed that, and repeating it is how a crash-looping
		// container got reported as provisioning.
		cause.note(fmt.Sprintf(
			"Datum has not reported back on this either way — not success, not failure (%s is still "+
				"unknown) — so %q says what is meant to be happening, not what anyone saw.",
			c.Type, c.Reason))
	}
	cause.note(discardedTime(c, cause.LastTransitionTime))
	return cause
}

// noTerminalStateReported recognises the staging shape: the object is not
// reaching ready, its condition status is Unknown so nothing has reported
// success or failure, it has existed for longer than its own reason's window,
// and the condition clock either already contradicts the reason or cannot be
// read at all.
//
// That last clause is what keeps this off a healthy object that broke a moment
// ago. An object can be nine days old and have failed one minute ago; age alone
// says nothing, which is why the creation floor never escalates actionability
// by itself.
func noTerminalStateReported(obj object, cause Cause, info ReasonInfo, objectAge, inState time.Duration) bool {
	if cause.Reported || !obj.notReady || info.ExpectedDuration <= 0 {
		return false
	}
	if objectAge <= info.ExpectedDuration {
		return false
	}
	return cause.LastTransitionTime == "" || inState > info.ExpectedDuration
}

// applyNoTerminalState rewrites the cause's narrative to separate what was
// observed from what is inferred, and to stop the catalogued explanation
// asserting provider activity that nothing ever reported.
func applyNoTerminalState(cause *Cause, info ReasonInfo) {
	cause.Pattern = PatternNoTerminalStateReported

	clock := "and it carries no usable timestamp"
	if cause.InStateFor != "" {
		clock = fmt.Sprintf("though its status was last rewritten %s ago", cause.InStateFor)
	}
	cause.note(fmt.Sprintf(
		"What is known: %s %s has existed for %s and has never come up, and Datum has not reported "+
			"back on it either way — not success, not failure (%s is still unknown) — %s.",
		cause.Level, cause.Object, cause.ObjectAge, cause.ConditionType, clock))
	cause.note(fmt.Sprintf(
		"What that suggests: something is working on it and never saying how it turned out. %q is "+
			"what is meant to be happening, not what anyone saw, so it is no evidence that your "+
			"workload itself is fine.", cause.Reason))

	rule := "Nothing has been reported either way, so neither your workload nor Datum is ruled out yet."
	if cause.Level == LevelInstance {
		rule = fmt.Sprintf(
			"From here, a container of your own that starts and exits immediately (InstanceCrashing, "+
				"which you would fix yourself) looks exactly like Datum infrastructure that never "+
				"reported back. Read the instance logs and exit code first — the %q steps — and "+
				"raise it with Datum at the same time.", SkillInstanceNotReady)
	}
	cause.Remediation = fmt.Sprintf(
		"Stop waiting on this one, and stop treating it as work in progress. %s has been failing "+
			"for at least %s, where a step like this normally finishes within %s, and nothing has "+
			"reported back on it. %s",
		cause.Object, cause.FailingFor, info.ExpectedWithin, rule)
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
		return fmt.Sprintf("Workload %s is running: %d of %d replicas ready.",
			w.Name, w.Status.ReadyReplicas, w.Status.DesiredReplicas)
	}
	if root == nil {
		return fmt.Sprintf(
			"Workload %s reports nothing wrong, but only %d of %d replicas are ready.",
			w.Name, w.Status.ReadyReplicas, w.Status.DesiredReplicas)
	}

	var who string
	switch root.Actionability {
	case ActionabilityUser:
		who = "This is one you can fix yourself"
	case ActionabilityPlatform:
		who = "This one is Datum's to fix, not yours — take it to them"
	case ActionabilityTransient:
		who = "This is a normal step along the way and should clear on its own"
	case ActionabilityStalled:
		// Says what has actually been established and what has not. The
		// classification alone reads as "not your fault", which is the
		// overclaim a crash-looping container punishes. Whether anything was
		// reported changes which half is true, so the two never share a
		// sentence.
		who = "A step like this normally finishes quickly and this one has not, so treat it as " +
			"stuck rather than in progress and take it to Datum"
		if root.Reported {
			who += ". Datum did report this state, so quote what it said — but nothing accounts " +
				"for the delay"
		} else {
			who += ". Nothing has reported a fault either way, so a problem inside your own " +
				"container is not ruled out"
		}
	default:
		who = "Who has to act on this one is not something Datum has classified"
	}
	if root.Pattern == PatternNoTerminalStateReported {
		// Overrides the actionability sentence rather than adding to it: an
		// unreported reason that says "in progress" must not be summarised as a
		// normal step, and blaming Datum is exactly the overclaim the
		// crash-looping container case punishes.
		who = "Treat this as stuck rather than in progress: nothing has reported back either way, " +
			"so it is worth taking to Datum with the details below — with the caveat that a " +
			"container of your own that keeps crashing on start would look exactly like this from " +
			"here, so that is not ruled out"
	}

	state := "is not running"
	if available {
		state = "is only partly running"
	}
	// The age belongs in the one-line summary: it is what separates work that
	// is in flight from work that stopped moving days ago. Both numbers are
	// carried when they disagree, because the disagreement is the finding.
	var held string
	switch {
	case root.FailingFor != "" && root.InStateFor == "":
		held = fmt.Sprintf(", stuck for at least %s (there is no usable timestamp on its status)",
			root.FailingFor)
	case root.FailingFor != "" && root.FailingFor != root.InStateFor:
		held = fmt.Sprintf(", unchanged for %s but stuck for at least %s",
			root.InStateFor, root.FailingFor)
	case root.InStateFor != "":
		held = fmt.Sprintf(", unchanged for %s", root.InStateFor)
	}
	return fmt.Sprintf(
		"Workload %s %s: %d of %d replicas ready. The hold-up is %s %s%s (status %s on %s). %s.",
		w.Name, state, w.Status.ReadyReplicas, w.Status.DesiredReplicas,
		root.Level, root.Object, held, root.Reason, root.ConditionType, who)
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
			"Do not change your workload to work around this one: Datum reported the fault as "+
				"theirs, and no edit on your side clears it.")
	}
	if root.Actionability == ActionabilityStalled {
		step := "Do not describe this as normal progress. The status still says the work is " +
			"under way, but it has taken far longer than that work should take."
		// Whether anything reported at all decides what may be asserted, so the
		// two statuses get different advice instead of one blended line.
		if root.Reported {
			step += " Datum did report this state, so quote its status message as what was reported."
		} else {
			step += " Nothing has reported a cause at all, so gather the details rather than " +
				"naming a culprit."
		}
		steps = append(steps, step)
	}
	if root.Pattern == PatternNoTerminalStateReported {
		steps = append(steps, fmt.Sprintf(
			"Keep what is known apart from what is guessed. Known: %s is %s old and Datum has "+
				"still reported nothing about %s either way. Guessed, and not established: that "+
				"Datum's infrastructure is the thing at fault.",
			root.Object, root.ObjectAge, root.ConditionType))
		if root.Level == LevelInstance {
			steps = append(steps, fmt.Sprintf(
				"Rule out a crashing container (InstanceCrashing) before saying this cannot be a "+
					"problem with the workload itself — the %q steps cover how. A container of "+
					"your own that exits the moment it starts looks, from here, exactly like "+
					"Datum infrastructure that never reported back.", SkillInstanceNotReady))
		}
	}
	if root.Skill != "" {
		steps = append(steps, fmt.Sprintf("The %q runbook has the full procedure.", root.Skill))
	}
	return steps
}
