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
	if !strings.Contains(d.Summary, "is available") {
		t.Errorf("Summary = %q, want it to say the workload is available", d.Summary)
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
	if !strings.Contains(d.Summary, "escalate to Datum") {
		t.Errorf("Summary = %q, want it to direct the reader to escalate", d.Summary)
	}

	var warned bool
	for _, s := range d.NextSteps {
		if strings.Contains(s, "Do not change the workload spec") {
			warned = true
		}
	}
	if !warned {
		t.Errorf("NextSteps = %q, want an explicit do-not-change-the-spec warning", d.NextSteps)
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
	if !strings.Contains(d.RootCause.Explanation, "No catalog entry") {
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
