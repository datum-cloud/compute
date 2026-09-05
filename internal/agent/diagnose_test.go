package agent

import (
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// testNamespace is the project namespace every fixture in this file lives in.
const testNamespace = "demo-project"

func cond(condType, status, reason, message string) metav1.Condition {
	return metav1.Condition{
		Type:    condType,
		Status:  metav1.ConditionStatus(status),
		Reason:  reason,
		Message: message,
	}
}

// condAt is cond with the transition time pinned, for the tests that turn on
// how long a condition has held its current state.
func condAt(condType, status, reason, message string, at time.Time) metav1.Condition {
	c := cond(condType, status, reason, message)
	c.LastTransitionTime = metav1.NewTime(at)
	return c
}

func workload(name string, ready, desired int32, conditions ...metav1.Condition) *computev1alpha.Workload {
	w := &computev1alpha.Workload{}
	w.Name = name
	w.Namespace = testNamespace
	w.Status.ReadyReplicas = ready
	w.Status.DesiredReplicas = desired
	w.Status.Conditions = conditions
	return w
}

func deployment(name string, conditions ...metav1.Condition) computev1alpha.WorkloadDeployment {
	d := computev1alpha.WorkloadDeployment{}
	d.Name = name
	d.Namespace = testNamespace
	d.Status.Conditions = conditions
	return d
}

func instance(name string, conditions ...metav1.Condition) computev1alpha.Instance {
	i := computev1alpha.Instance{}
	i.Name = name
	i.Namespace = testNamespace
	i.Status.Conditions = conditions
	return i
}

// TestDiagnoseResolvesPointerToLeafCause is the central behaviour: compute's
// top-level reasons name the blocking subsystem, not the cause, and the walk
// must report the condition underneath that actually explains the failure.
func TestDiagnoseResolvesPointerToLeafCause(t *testing.T) {
	tests := []struct {
		name              string
		workload          *computev1alpha.Workload
		deployments       []computev1alpha.WorkloadDeployment
		instances         []computev1alpha.Instance
		wantReason        string
		wantLevel         Level
		wantActionability Actionability
		wantSkill         string
	}{
		{
			name: "quota: QuotaNotGranted points at the instance's QuotaExceeded",
			workload: workload("api-backend", 2, 6,
				cond(computev1alpha.WorkloadAvailable, "False",
					computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, "Quota is blocking 4 of 6 instances.")),
			deployments: []computev1alpha.WorkloadDeployment{
				deployment("api-backend-a",
					cond(computev1alpha.WorkloadDeploymentAvailable, "False",
						computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, "Quota is blocking 4 instances.")),
			},
			instances: []computev1alpha.Instance{
				instance("api-backend-a-e5f6",
					cond(computev1alpha.InstanceReady, "False",
						computev1alpha.InstanceReadyReasonSchedulingGatesPresent, "Gated pending quota."),
					cond(computev1alpha.InstanceQuotaGranted, "False",
						computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
						"Requested 4 vCPU / 16Gi; 2 vCPU / 8Gi remaining.")),
			},
			wantReason:        computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
			wantLevel:         LevelInstance,
			wantActionability: ActionabilityUser,
			wantSkill:         SkillQuotaTriage,
		},
		{
			name: "placement: NoAvailablePlacements points at the deployment's NoMatchingLocation",
			workload: workload("edge-cache", 0, 4,
				cond(computev1alpha.WorkloadAvailable, "False",
					computev1alpha.WorkloadReasonNoAvailablePlacements, "All placements report no available deployments.")),
			deployments: []computev1alpha.WorkloadDeployment{
				deployment("edge-cache-ams",
					cond(computev1alpha.WorkloadDeploymentAvailable, "False",
						computev1alpha.WorkloadDeploymentReasonNoMatchingLocation,
						"The cell has not been told which location it serves.")),
			},
			wantReason:        computev1alpha.WorkloadDeploymentReasonNoMatchingLocation,
			wantLevel:         LevelDeployment,
			wantActionability: ActionabilityPlatform,
			wantSkill:         SkillPlacementTriage,
		},
		{
			name: "image: InstancesProvisioning points at the instance's ImageUnavailable",
			workload: workload("batch-processor", 0, 2,
				cond(computev1alpha.WorkloadAvailable, "False",
					computev1alpha.WorkloadReasonNoAvailablePlacements, "All placements report no available deployments.")),
			deployments: []computev1alpha.WorkloadDeployment{
				deployment("batch-processor-eu",
					cond(computev1alpha.WorkloadDeploymentAvailable, "False",
						computev1alpha.WorkloadDeploymentReasonInstancesProvisioning, "Instances exist but none are ready.")),
			},
			instances: []computev1alpha.Instance{
				instance("batch-processor-eu-q1r2",
					cond(computev1alpha.InstanceReady, "False",
						computev1alpha.InstanceReadyReasonImageUnavailable, `Failed to pull image: manifest unknown.`)),
			},
			wantReason:        computev1alpha.InstanceReadyReasonImageUnavailable,
			wantLevel:         LevelInstance,
			wantActionability: ActionabilityUser,
			wantSkill:         SkillInstanceNotReady,
		},
		{
			name: "referenced data: ReferencedDataNotReady points at SourceNotFound",
			workload: workload("config-consumer", 0, 1,
				cond(computev1alpha.WorkloadAvailable, "False",
					computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady, `ConfigMap "app-config" not found.`)),
			deployments: []computev1alpha.WorkloadDeployment{
				deployment("config-consumer-a",
					cond(computev1alpha.ReferencedDataReady, "False",
						computev1alpha.ReferencedDataReasonSourceNotFound, `ConfigMap "app-config" not found.`)),
			},
			wantReason:        computev1alpha.ReferencedDataReasonSourceNotFound,
			wantLevel:         LevelDeployment,
			wantActionability: ActionabilityUser,
			wantSkill:         SkillReferencedData,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := Diagnose(tc.workload, tc.deployments, tc.instances)
			if d.RootCause == nil {
				t.Fatalf("RootCause is nil, want reason %q", tc.wantReason)
			}
			if got := d.RootCause.Reason; got != tc.wantReason {
				t.Errorf("RootCause.Reason = %q, want %q (a pointer reason was reported as the cause)", got, tc.wantReason)
			}
			if got := d.RootCause.Level; got != tc.wantLevel {
				t.Errorf("RootCause.Level = %q, want %q", got, tc.wantLevel)
			}
			if got := d.RootCause.Actionability; got != tc.wantActionability {
				t.Errorf("RootCause.Actionability = %q, want %q", got, tc.wantActionability)
			}
			if got := d.SuggestedSkill; got != tc.wantSkill {
				t.Errorf("SuggestedSkill = %q, want %q", got, tc.wantSkill)
			}
			if IsPointerReason(d.RootCause.Reason) {
				t.Errorf("RootCause.Reason %q is a pointer reason and must never be the answer", d.RootCause.Reason)
			}
		})
	}
}

func TestDiagnoseHealthyWorkload(t *testing.T) {
	w := workload("web-frontend", 3, 3,
		cond(computev1alpha.WorkloadAvailable, "True",
			computev1alpha.WorkloadDeploymentReasonStableInstanceFound, "At least one instance is ready."))
	insts := []computev1alpha.Instance{
		instance("web-frontend-a-1", cond(computev1alpha.InstanceReady, "True", computev1alpha.InstanceReadyReasonAvailable, "Serving.")),
		instance("web-frontend-a-2", cond(computev1alpha.InstanceReady, "True", computev1alpha.InstanceReadyReasonAvailable, "Serving.")),
	}

	d := Diagnose(w, nil, insts)
	if !d.Available {
		t.Error("Available = false, want true")
	}
	if d.RootCause != nil {
		t.Errorf("RootCause = %+v, want nil for a healthy workload", d.RootCause)
	}
	if d.Instances.Ready != 2 || len(d.Instances.Blocked) != 0 {
		t.Errorf("Instances = %+v, want 2 ready and none blocked", d.Instances)
	}
	if !strings.Contains(d.Summary, "is running") {
		t.Errorf("Summary = %q, want it to say the workload is running", d.Summary)
	}
}

// TestDiagnosePlatformFaultTellsCustomerNotToChangeSpec guards the advice that
// matters most: for a platform fault, editing the workload cannot help, and
// saying otherwise sends the customer down a dead end.
func TestDiagnosePlatformFaultTellsCustomerNotToChangeSpec(t *testing.T) {
	w := workload("edge-cache", 0, 4,
		cond(computev1alpha.WorkloadAvailable, "False",
			computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments."))
	deps := []computev1alpha.WorkloadDeployment{
		deployment("edge-cache-ams",
			cond(computev1alpha.WorkloadDeploymentAvailable, "False",
				computev1alpha.WorkloadDeploymentReasonCityCodeMismatch, "Deployment asked for AMS; cell serves LHR.")),
	}

	d := Diagnose(w, deps, nil)
	if d.RootCause.Actionability != ActionabilityPlatform {
		t.Fatalf("Actionability = %q, want platform", d.RootCause.Actionability)
	}
	if !strings.Contains(d.Summary, "Datum's to fix") {
		t.Errorf("Summary = %q, want it to direct the reader to take this to Datum", d.Summary)
	}

	var warned bool
	for _, s := range d.NextSteps {
		if strings.Contains(s, "Do not change your workload") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("NextSteps = %q, want an explicit do-not-change-your-workload warning", d.NextSteps)
	}
}

// TestDiagnoseBlockedInstancesSkipPointerReasons checks the per-instance
// summary reports each instance's real reason, not its scheduling gate.
func TestDiagnoseBlockedInstancesSkipPointerReasons(t *testing.T) {
	w := workload("api-backend", 1, 3,
		cond(computev1alpha.WorkloadAvailable, "False",
			computev1alpha.WorkloadDeploymentReasonQuotaNotGranted, "Quota blocking."))
	insts := []computev1alpha.Instance{
		instance("ok-1", cond(computev1alpha.InstanceReady, "True", computev1alpha.InstanceReadyReasonAvailable, "Serving.")),
		instance("blocked-1",
			cond(computev1alpha.InstanceReady, "False",
				computev1alpha.InstanceReadyReasonSchedulingGatesPresent, "Gated."),
			cond(computev1alpha.InstanceQuotaGranted, "False",
				computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded, "No headroom.")),
	}

	d := Diagnose(w, nil, insts)
	if d.Instances.Ready != 1 {
		t.Errorf("Ready = %d, want 1", d.Instances.Ready)
	}
	if len(d.Instances.Blocked) != 1 {
		t.Fatalf("Blocked = %d, want 1", len(d.Instances.Blocked))
	}
	if got := d.Instances.Blocked[0].Reason; got != computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded {
		t.Errorf("Blocked[0].Reason = %q, want the real cause QuotaExceeded, not the gate", got)
	}
}

// TestDiagnoseUncataloguedReason checks an unknown reason still produces a
// usable answer rather than a confident wrong classification.
func TestDiagnoseUncataloguedReason(t *testing.T) {
	w := workload("mystery", 0, 1,
		cond(computev1alpha.WorkloadAvailable, "False", "SomeBrandNewReason", "Something new happened."))

	d := Diagnose(w, nil, nil)
	if d.RootCause == nil {
		t.Fatal("RootCause is nil; an uncatalogued reason should still be reported")
	}
	if d.RootCause.Actionability != "" {
		t.Errorf("Actionability = %q, want empty rather than a guessed classification", d.RootCause.Actionability)
	}
	if !strings.Contains(d.RootCause.Explanation, "no explanation written for") {
		t.Errorf("Explanation = %q, want it to admit the reason is uncatalogued", d.RootCause.Explanation)
	}
}

// TestDiagnoseReportsHowLongTheCauseHasHeld covers the gap that made a wedged
// workload indistinguishable from a healthy one: a condition's age has to reach
// the answer, and it has to be legible without arithmetic.
func TestDiagnoseReportsHowLongTheCauseHasHeld(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name           string
		since          time.Duration
		wantInStateFor string
	}{
		{"seconds", 30 * time.Second, "30s"},
		{"minutes", 12 * time.Minute, "12m"},
		{"hours", 3*time.Hour + 20*time.Minute, "3h20m"},
		{"whole hours", 4 * time.Hour, "4h"},
		{"days", 5 * 24 * time.Hour, "5d"},
		{"days and hours", 5*24*time.Hour + 6*time.Hour, "5d6h"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := workload("xcheck-iad", 0, 1,
				condAt(computev1alpha.WorkloadAvailable, "False",
					computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
					"Programming in progress.", now.Add(-tc.since)))

			d := DiagnoseAt(now, w, nil, nil)
			if d.RootCause.InStateFor != tc.wantInStateFor {
				t.Errorf("InStateFor = %q, want %q", d.RootCause.InStateFor, tc.wantInStateFor)
			}
			if d.RootCause.LastTransitionTime == "" {
				t.Error("LastTransitionTime is empty; the absolute timestamp must survive too")
			}
			if !strings.Contains(d.Summary, tc.wantInStateFor) {
				t.Errorf("Summary = %q, want it to carry the age %q", d.Summary, tc.wantInStateFor)
			}
		})
	}
}

// TestDiagnoseWithoutTransitionTimeReportsNoAge: the API may leave
// LastTransitionTime unset, and an invented age is worse than none.
func TestDiagnoseWithoutTransitionTimeReportsNoAge(t *testing.T) {
	w := workload("mystery", 0, 1,
		cond(computev1alpha.WorkloadAvailable, "False",
			computev1alpha.InstanceProgrammedReasonProgrammingInProgress, "Programming."))

	d := DiagnoseAt(time.Now(), w, nil, nil)
	if d.RootCause.InStateFor != "" {
		t.Errorf("InStateFor = %q, want empty when the API set no transition time", d.RootCause.InStateFor)
	}
	if d.RootCause.LastTransitionTime != "" {
		t.Errorf("LastTransitionTime = %q, want empty", d.RootCause.LastTransitionTime)
	}
}

// ---------------------------------------------------------------------------
// The staging Instances of 2026-08-27/28. Every layer above them was healthy,
// the pod on the cell was crash-looping, and the provider never propagated that
// upward: the project control plane still read Programmed=Unknown /
// ProgrammingInProgress, with condition timestamps that were either the Unix
// epoch or hours old on an object nine days old.

// stagingNow is the instant the three stuck workloads were traced to the
// crashing container.
var stagingNow = time.Date(2026, 9, 5, 12, 46, 24, 0, time.UTC)

// stagingInState is how long the staging conditions claimed to have held, on
// objects that had in fact been broken for nine days. The gap between the two
// numbers is the finding, so both are asserted in several places from here.
const stagingInState = "9h30m"

// epochCond is a condition carrying the sentinel real Instances arrive with.
// It is not metav1.Time's zero value — Go's zero time is year 1 — so IsZero()
// does not catch it.
func epochCond(condType, status, reason, message string) metav1.Condition {
	c := cond(condType, status, reason, message)
	c.LastTransitionTime = metav1.NewTime(time.Unix(0, 0).UTC())
	return c
}

func instanceCreated(name string, created time.Time, conditions ...metav1.Condition) computev1alpha.Instance {
	i := instance(name, conditions...)
	i.CreationTimestamp = metav1.NewTime(created)
	return i
}

// workloadCreated is a single-replica Workload with its creationTimestamp set.
// The staging workloads were all one replica, none of it ready.
func workloadCreated(name string, created time.Time, conditions ...metav1.Condition) *computev1alpha.Workload {
	w := workload(name, 0, 1, conditions...)
	w.CreationTimestamp = metav1.NewTime(created)
	return w
}

// TestDiagnoseDiscardsImplausibleTimestamps covers defect 1. demo2loc-dfw-dfw-1
// carried lastTransitionTime "1970-01-01T00:00:00Z" on Programmed, Available
// and Ready. metav1.Time.IsZero() let it through, and the epoch renders as
// 496846h — twenty thousand days — which alone was enough to call the condition
// stalled. An implausible timestamp is no signal, not a huge one.
func TestDiagnoseDiscardsImplausibleTimestamps(t *testing.T) {
	created := stagingNow.Add(-9 * 24 * time.Hour)

	tests := []struct {
		name     string
		instance computev1alpha.Instance
	}{
		{
			name: "the epoch sentinel, on an object with no creationTimestamp",
			instance: instance("demo2loc-dfw-dfw-1",
				epochCond(computev1alpha.InstanceProgrammed, "Unknown",
					computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
					"Instance is provisioning")),
		},
		{
			name: "the epoch sentinel, on the real nine-day-old object",
			instance: instanceCreated("demo2loc-dfw-dfw-1", created,
				epochCond(computev1alpha.InstanceProgrammed, "Unknown",
					computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
					"Instance is provisioning")),
		},
		{
			name: "a timestamp predating the object that carries it",
			instance: instanceCreated("demo2loc-dfw-dfw-1", created,
				condAt(computev1alpha.InstanceProgrammed, "Unknown",
					computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
					"Instance is provisioning", created.Add(-time.Hour))),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := workloadCreated("demo2loc", created,
				cond(computev1alpha.WorkloadAvailable, "False",
					computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments."))

			d := DiagnoseAt(stagingNow, w, nil, []computev1alpha.Instance{tc.instance})
			root := d.RootCause

			if root.LastTransitionTime != "" {
				t.Errorf("LastTransitionTime = %q, want empty: the value cannot be believed",
					root.LastTransitionTime)
			}
			if root.InStateFor != "" {
				t.Errorf("InStateFor = %q, want empty rather than an age derived from a sentinel",
					root.InStateFor)
			}
			if root.Actionability == ActionabilityStalled {
				t.Error("Actionability = stalled, derived from a timestamp that is not a fact")
			}
			// The whole diagnosis is what the assistant reads, so no age
			// derived from the sentinel may leak into any of it. The sentinel
			// itself is quoted once, deliberately, as evidence.
			for _, text := range append([]string{d.Summary, root.Explanation, root.Remediation}, d.NextSteps...) {
				for _, absurd := range []string{"20701", "496846"} {
					if strings.Contains(text, absurd) {
						t.Errorf("output contains %q, an age derived from the epoch sentinel: %q",
							absurd, text)
					}
				}
			}
			if !strings.Contains(root.Explanation, "placeholder") {
				t.Errorf("Explanation = %q, want it to say the timestamp was discarded and why",
					root.Explanation)
			}
		})
	}
}

// TestActionabilityAtIgnoresTheEpochSentinel is the same defect at the boundary
// the escalation is actually decided at.
func TestActionabilityAtIgnoresTheEpochSentinel(t *testing.T) {
	programming, ok := ExplainReason(computev1alpha.InstanceProgrammedReasonProgrammingInProgress)
	if !ok {
		t.Fatal("ProgrammingInProgress should be catalogued")
	}
	if got := ActionabilityAt(programming, "1970-01-01T00:00:00Z", stagingNow); got != ActionabilityTransient {
		t.Errorf("ActionabilityAt(epoch) = %q, want %q — 20701 days is a sentinel, not a stall",
			got, ActionabilityTransient)
	}
}

// TestDiagnoseUsesCreationTimestampAsFailureFloor covers defect 2. rootCauseFor
// reported 9h30m for workloads that had been failing since 2026-08-27, because
// every crash rewrites lastTransitionTime and resets the clock. The object's own
// creationTimestamp cannot be reset that way.
func TestDiagnoseUsesCreationTimestampAsFailureFloor(t *testing.T) {
	created := stagingNow.Add(-9 * 24 * time.Hour)
	rewritten := stagingNow.Add(-9*time.Hour - 30*time.Minute)

	w := workloadCreated("xcheck", created,
		cond(computev1alpha.WorkloadAvailable, "False",
			computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments."))
	insts := []computev1alpha.Instance{
		instanceCreated("xcheck-dfw-dfw-0", created,
			condAt(computev1alpha.InstanceProgrammed, "Unknown",
				computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
				"Instance is provisioning", rewritten)),
	}

	d := DiagnoseAt(stagingNow, w, nil, insts)
	root := d.RootCause

	if root.InStateFor != stagingInState {
		t.Errorf("InStateFor = %q, want %q — the condition clock is still reported as it is",
			root.InStateFor, stagingInState)
	}
	if root.ObjectAge != "9d" {
		t.Errorf("ObjectAge = %q, want %q", root.ObjectAge, "9d")
	}
	if root.FailingFor != "9d" {
		t.Errorf("FailingFor = %q, want %q — the object has been broken nine days, whatever the "+
			"condition says", root.FailingFor, "9d")
	}
	// The gap is the diagnostic signal, so both numbers have to reach the
	// summary; reporting only the smaller one is the defect.
	if !strings.Contains(d.Summary, stagingInState) || !strings.Contains(d.Summary, "9d") {
		t.Errorf("Summary = %q, want it to carry both 9h30m and 9d", d.Summary)
	}
}

// A ready object is not "failing", however old it is: the floor is a floor on
// an outage, not on the object's existence.
func TestDiagnoseDoesNotClaimAReadyObjectIsFailing(t *testing.T) {
	created := stagingNow.Add(-9 * 24 * time.Hour)
	w := workload("served", 1, 1,
		cond(computev1alpha.WorkloadAvailable, "True",
			computev1alpha.WorkloadDeploymentReasonStableInstanceFound, "Serving."))
	w.CreationTimestamp = metav1.NewTime(created)
	insts := []computev1alpha.Instance{
		instanceCreated("served-dfw-0", created,
			cond(computev1alpha.InstanceReady, "True", computev1alpha.InstanceReadyReasonAvailable, "Serving."),
			condAt(computev1alpha.InstanceQuotaGranted, "False",
				computev1alpha.InstanceQuotaGrantedReasonPendingEvaluation, "Evaluating.",
				stagingNow.Add(-time.Minute))),
	}

	d := DiagnoseAt(stagingNow, w, nil, insts)
	if d.RootCause.FailingFor != "" {
		t.Errorf("FailingFor = %q, want empty: the instance is Ready", d.RootCause.FailingFor)
	}
}

// TestDiagnoseSeparatesUnreportedFromReportedFailure covers defect 3.
// Programmed=Unknown means no controller has reported success or failure. That
// is not "provisioning", and it is not evidence that the spec is sound — the
// pod behind these Instances was crash-looping the whole time, which is
// InstanceCrashing, a user-actionable class.
func TestDiagnoseSeparatesUnreportedFromReportedFailure(t *testing.T) {
	created := stagingNow.Add(-9 * 24 * time.Hour)
	rewritten := stagingNow.Add(-9*time.Hour - 30*time.Minute)

	diagnose := func(status string) Diagnosis {
		w := workloadCreated("joseszycho-billing-test", created,
			cond(computev1alpha.WorkloadAvailable, "False",
				computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments."))
		insts := []computev1alpha.Instance{
			instanceCreated("joseszycho-billing-test-dfw-dfw-0", created,
				condAt(computev1alpha.InstanceProgrammed, status,
					computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
					"Instance is provisioning", rewritten)),
		}
		return DiagnoseAt(stagingNow, w, nil, insts)
	}

	t.Run("Unknown is nothing reported", func(t *testing.T) {
		d := diagnose("Unknown")
		root := d.RootCause

		if root.Reported {
			t.Error("Reported = true for a condition status of Unknown")
		}
		if root.Pattern != PatternNoTerminalStateReported {
			t.Errorf("Pattern = %q, want %q", root.Pattern, PatternNoTerminalStateReported)
		}
		// The two labels are the observed/inferred split in plain words. They
		// are asserted because collapsing them is how "nothing was reported"
		// became "the provider is at fault".
		if !strings.Contains(root.Explanation, "What is known:") ||
			!strings.Contains(root.Explanation, "What that suggests:") {
			t.Errorf("Explanation = %q, want it to separate what is known from what it suggests",
				root.Explanation)
		}
		// The assistant told the customer "this isn't a spec problem, escalate
		// to Datum" while the container was crash-looping. Naming the
		// user-actionable possibility is the point of the whole change.
		if !strings.Contains(root.Remediation, "InstanceCrashing") {
			t.Errorf("Remediation = %q, want it to name InstanceCrashing as a live possibility",
				root.Remediation)
		}
		var routed bool
		for _, s := range d.NextSteps {
			if strings.Contains(s, SkillInstanceNotReady) {
				routed = true
			}
		}
		if !routed {
			t.Errorf("NextSteps = %q, want the instance-not-ready runbook offered alongside escalation",
				d.NextSteps)
		}
		if strings.Contains(root.Remediation, "this is normal") {
			t.Errorf("Remediation still tells the reader this is normal: %q", root.Remediation)
		}
	})

	t.Run("False is a reported failure", func(t *testing.T) {
		root := diagnose("False").RootCause

		if !root.Reported {
			t.Error("Reported = false for a condition status of False")
		}
		if root.Pattern != "" {
			t.Errorf("Pattern = %q, want empty: a controller did report this status", root.Pattern)
		}
		if strings.Contains(root.Explanation, "has not reported back") {
			t.Errorf("Explanation = %q, want it not to claim nothing was reported", root.Explanation)
		}
	})
}

// The pattern must not fire on an object that is merely old. Nine days of
// healthy service followed by a two-minute-old Unknown is in-flight work, and
// calling it stuck is the same overclaim in the other direction.
func TestDiagnoseDoesNotFlagAFreshFailureOnAnOldObject(t *testing.T) {
	created := stagingNow.Add(-9 * 24 * time.Hour)
	w := workloadCreated("recently-broken", created,
		cond(computev1alpha.WorkloadAvailable, "False",
			computev1alpha.WorkloadReasonNoAvailablePlacements, "No available deployments."))
	insts := []computev1alpha.Instance{
		instanceCreated("recently-broken-dfw-0", created,
			condAt(computev1alpha.InstanceProgrammed, "Unknown",
				computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
				"Instance is provisioning", stagingNow.Add(-2*time.Minute))),
	}

	d := DiagnoseAt(stagingNow, w, nil, insts)
	if d.RootCause.Pattern != "" {
		t.Errorf("Pattern = %q, want empty for a two-minute-old condition", d.RootCause.Pattern)
	}
	if d.RootCause.Actionability != ActionabilityTransient {
		t.Errorf("Actionability = %q, want transient", d.RootCause.Actionability)
	}
}
