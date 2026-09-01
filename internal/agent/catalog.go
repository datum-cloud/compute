// Package agent turns compute's condition status into answers an assistant can
// give a customer.
//
// Compute already carries the diagnostic vocabulary: every blocking cause is a
// stable, machine-readable reason on a condition, and the doc comments in
// api/v1alpha say, for each one, whether the customer must act, whether the
// platform is at fault, or whether it is simply in flight. This package lifts
// that into a lookup table (catalog.go) and a walk over the resource tree
// (diagnose.go).
package agent

import (
	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Actionability says who has to do something about a condition.
type Actionability string

const (
	// ActionabilityUser means the workload spec or project configuration is
	// wrong and the customer can fix it.
	ActionabilityUser Actionability = "user"

	// ActionabilityPlatform means the cause is Datum's. No change to the
	// workload will help, and suggesting one wastes the customer's time.
	ActionabilityPlatform Actionability = "platform"

	// ActionabilityTransient means the resource is in a normal in-flight state
	// and the right advice is to wait.
	ActionabilityTransient Actionability = "transient"
)

// ReasonInfo explains one condition reason.
type ReasonInfo struct {
	// Reason is the condition reason as written by the controllers.
	Reason string
	// ConditionTypes are the condition types this reason is observed on.
	ConditionTypes []string
	// Actionability says who must act.
	Actionability Actionability
	// Explanation states in plain language what actually happened.
	Explanation string
	// Remediation says what to do next. Empty for healthy reasons.
	Remediation string
	// Skill names the runbook covering this class of failure, if any.
	Skill string
}

// Remediation strings shared by several reasons.
const (
	// remediationWait is the advice for transient states: the resource is in
	// flight and nothing needs doing.
	remediationWait = "Wait."
	// remediationEscalate is the advice for platform faults the customer has
	// no lever on at all.
	remediationEscalate = "Escalate to Datum."
)

// Skill names. These correspond to the runbooks published alongside this
// package; a binding advertises them and the assistant loads one on demand.
const (
	SkillWorkloadNotAvailable = "workload-not-available"
	SkillQuotaTriage          = "quota-triage"
	SkillInstanceNotReady     = "instance-not-ready"
	SkillReferencedData       = "referenced-data-triage"
	SkillPlacementTriage      = "placement-triage"
)

// catalog is the full reason vocabulary. Entries key off the constants in
// api/v1alpha rather than string literals so that this file and the API cannot
// drift apart silently; TestCatalogCoversEveryAPIReason enforces completeness.
//
// Several reasons share a string across condition types (ImageUnavailable is
// set on both Ready and Programmed, for example). One entry covers all of them.
var catalog = []ReasonInfo{
	// ------------------------------------------------------------- healthy
	{
		Reason:         computev1alpha.InstanceReadyReasonAvailable,
		ConditionTypes: []string{computev1alpha.InstanceReady, computev1alpha.InstanceAvailable},
		Actionability:  ActionabilityTransient,
		Explanation:    "The instance is serving.",
	},
	{
		Reason:         computev1alpha.WorkloadDeploymentReasonStableInstanceFound,
		ConditionTypes: []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:  ActionabilityTransient,
		Explanation:    "The deployment has at least one ready instance and is serving.",
	},
	{
		Reason:         computev1alpha.InstanceProgrammedReasonProgrammed,
		ConditionTypes: []string{computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityTransient,
		Explanation:    "The infrastructure provider has fully programmed the instance.",
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityTransient,
		Explanation:    "Quota was evaluated and granted for this instance.",
	},
	{
		Reason:         computev1alpha.ReferencedDataReasonReady,
		ConditionTypes: []string{computev1alpha.ReferencedDataReady},
		Actionability:  ActionabilityTransient,
		Explanation:    "All referenced ConfigMaps and Secrets are resolved and present on the cell.",
	},

	// --------------------------------------------------------------- quota
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityUser,
		Explanation: "The project asked for more compute than its allowance permits, so the quota " +
			"backend explicitly denied the claim. Nothing will proceed until headroom exists.",
		Remediation: "Reduce the workload's replica count or instance size, release capacity by " +
			"deleting unused workloads, or request a quota increase for the project.",
		Skill: SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonNoBudget,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation: "The quota claim was created and is pending because no AllowanceBucket has been " +
			"configured for this project at all. This is distinct from QuotaExceeded (explicitly " +
			"denied) and PendingEvaluation (evaluation in flight) — the project has no budget to " +
			"spend against.",
		Remediation: "The project needs an AllowanceBucket provisioned. Escalate to Datum — the " +
			"customer cannot configure this.",
		Skill: SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonPendingEvaluation,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityTransient,
		Explanation:    "The quota claim has not been created yet, or its first evaluation is still in flight.",
		Remediation:    "Wait. If it persists for more than a few minutes, treat it as a quota-backend problem.",
		Skill:          SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonBackendUnavailable,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation: "Quota enforcement is configured but the Milo quota backend could not be reached — " +
			"network error, TLS failure, or a 401/503 from the backend.",
		Remediation: "Escalate to Datum. Nothing in the customer's workload spec affects this.",
		Skill:       SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonProjectNotFound,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation:    "The Milo project referenced by this instance does not exist — the project control plane returned 404.",
		Remediation:    "Escalate to Datum; the project registration is missing or was removed.",
		Skill:          SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonNamespaceNotFound,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation:    "The claim namespace does not exist on the Milo project control plane.",
		Remediation:    remediationEscalate,
		Skill:          SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonMisconfigured,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation: "The Milo admission plugin rejected the ResourceClaim (403/422): the " +
			"ResourceRegistration is absent, or the claimingRules do not match.",
		Remediation: "Escalate to Datum — this is a platform quota configuration error.",
		Skill:       SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonProjectIDUnresolvable,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation: "The namespace label required to derive the Milo project ID is missing or " +
			"unreadable, so the claim cannot be attributed to a project.",
		Remediation: remediationEscalate,
		Skill:       SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonQuotaDisabled,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityTransient,
		Explanation: "Quota enforcement is intentionally switched off in this environment because no " +
			"credential path was configured. Not a failure.",
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonValidationFailed,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityUser,
		Explanation:    "The quota claim failed validation before it could be evaluated.",
		Remediation:    "Check the instance resource requests for invalid or unsupported values.",
		Skill:          SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceProgrammedReasonPendingQuota,
		ConditionTypes: []string{computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityTransient,
		Explanation: "Programming is deliberately held back until quota is granted. The real cause is " +
			"on the instance's QuotaGranted condition — read that one.",
		Remediation: "Diagnose the QuotaGranted condition; this reason only points at it.",
		Skill:       SkillQuotaTriage,
	},

	// ---------------------------------------------------- instance runtime
	{
		Reason:         computev1alpha.InstanceReadyReasonImageUnavailable,
		ConditionTypes: []string{computev1alpha.InstanceReady, computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityUser,
		Explanation: "The provider could not pull the instance image: a bad image name, missing " +
			"registry credentials, or an unreachable registry.",
		Remediation: "Verify the image reference in the workload spec (tag exists, registry path " +
			"correct) and that pull credentials for a private registry are configured.",
		Skill: SkillInstanceNotReady,
	},
	{
		Reason:         computev1alpha.InstanceReadyReasonInstanceCrashing,
		ConditionTypes: []string{computev1alpha.InstanceReady, computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityUser,
		Explanation: "The process started but keeps exiting and being restarted (CrashLoopBackOff in " +
			"the underlying runtime). The application itself is failing — the platform delivered it " +
			"correctly.",
		Remediation: "Read the instance logs for the exit cause: a failing entrypoint, a missing env " +
			"var or mount, or an unmet dependency at startup.",
		Skill: SkillInstanceNotReady,
	},
	{
		Reason:         computev1alpha.InstanceReadyReasonConfigurationError,
		ConditionTypes: []string{computev1alpha.InstanceReady, computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityUser,
		Explanation: "The runtime rejected the instance configuration before the process could start — " +
			"for example an invalid environment-variable injection or a missing device.",
		Remediation: "Correct the workload spec; the runtime refused it as written.",
		Skill:       SkillInstanceNotReady,
	},
	{
		Reason:         computev1alpha.InstanceReadyReasonProvisioning,
		ConditionTypes: []string{computev1alpha.InstanceReady},
		Actionability:  ActionabilityTransient,
		Explanation:    "The runtime is still setting up the execution environment — creating the container, unpacking the image.",
		Remediation:    "Wait; this is normal and non-actionable.",
	},
	{
		Reason:         computev1alpha.InstanceReadyReasonSchedulingGatesPresent,
		ConditionTypes: []string{computev1alpha.InstanceReady},
		Actionability:  ActionabilityTransient,
		Explanation:    "Scheduling gates are still attached to the instance, so it is intentionally held before scheduling.",
		Remediation:    "Wait for the gates to clear; if they never do, the gating controller is stuck — escalate.",
	},
	{
		Reason:         computev1alpha.InstanceProgrammedReasonPendingProgramming,
		ConditionTypes: []string{computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityTransient,
		Explanation:    "The infrastructure provider has not started programming the instance yet.",
		Remediation:    "Wait. If it persists, the provider controller may not be running.",
	},
	{
		Reason:         computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
		ConditionTypes: []string{computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityTransient,
		Explanation:    "The infrastructure provider is actively programming the instance.",
		Remediation:    remediationWait,
	},
	{
		Reason:         computev1alpha.InstanceAvailableReasonStarting,
		ConditionTypes: []string{computev1alpha.InstanceAvailable},
		Actionability:  ActionabilityTransient,
		Explanation:    "The instance is starting up.",
		Remediation:    remediationWait,
	},
	{
		Reason:         computev1alpha.InstanceAvailableReasonStopping,
		ConditionTypes: []string{computev1alpha.InstanceAvailable},
		Actionability:  ActionabilityTransient,
		Explanation:    "The instance is shutting down.",
		Remediation:    remediationWait,
	},
	{
		Reason:         computev1alpha.InstanceAvailableReasonStopped,
		ConditionTypes: []string{computev1alpha.InstanceAvailable},
		Actionability:  ActionabilityUser,
		Explanation:    "The instance is stopped and is not serving.",
		Remediation:    "Start the instance, or check whether it was stopped deliberately.",
	},
	{
		Reason:         computev1alpha.InstanceReadyReasonSuspended,
		ConditionTypes: []string{computev1alpha.InstanceReady, computev1alpha.InstanceAvailable},
		Actionability:  ActionabilityPlatform,
		Explanation: "The instance was intentionally stopped because the project is suspended. Its " +
			"placement, disk, and quota allocation are retained and the process restarts from disk " +
			"on reinstatement.",
		Remediation: "Resolve the project suspension (usually billing or an account hold). No workload change will help.",
	},

	// --------------------------------------------------- referenced data
	{
		Reason:         computev1alpha.ReferencedDataReasonSourceNotFound,
		ConditionTypes: []string{computev1alpha.ReferencedDataReady},
		Actionability:  ActionabilityUser,
		Explanation: "One or more ConfigMaps or Secrets referenced by the workload template do not " +
			"exist in the project namespace.",
		Remediation: "Create the missing ConfigMap/Secret in the project namespace, or correct the " +
			"reference in the workload spec.",
		Skill: SkillReferencedData,
	},
	{
		Reason:         computev1alpha.ReferencedDataReasonSourceUnauthorized,
		ConditionTypes: []string{computev1alpha.ReferencedDataReady},
		Actionability:  ActionabilityPlatform,
		Explanation: "The management identity does not have permission to read one or more referenced " +
			"ConfigMaps or Secrets.",
		Remediation: "Escalate to Datum — the platform's RBAC for referenced data is insufficient.",
		Skill:       SkillReferencedData,
	},
	{
		Reason:         computev1alpha.ReferencedDataReasonSourceTooLarge,
		ConditionTypes: []string{computev1alpha.ReferencedDataReady},
		Actionability:  ActionabilityUser,
		Explanation:    "One or more referenced ConfigMaps or Secrets exceed the allowed size limit.",
		Remediation:    "Shrink the referenced object, or split it into smaller ones.",
		Skill:          SkillReferencedData,
	},
	{
		Reason:         computev1alpha.ReferencedDataReasonResolving,
		ConditionTypes: []string{computev1alpha.ReferencedDataReady},
		Actionability:  ActionabilityTransient,
		Explanation:    "The resolver is reading the source ConfigMaps/Secrets from the project control plane.",
		Remediation:    remediationWait,
	},
	{
		Reason:         computev1alpha.ReferencedDataReasonAwaitingPropagation,
		ConditionTypes: []string{computev1alpha.ReferencedDataReady},
		Actionability:  ActionabilityTransient,
		Explanation:    "The resolved data has not yet fully arrived on the cell.",
		Remediation:    remediationWait,
	},

	// ------------------------------------------- placement / deployment
	{
		Reason:         computev1alpha.WorkloadDeploymentReasonNoMatchingLocation,
		ConditionTypes: []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:  ActionabilityPlatform,
		Explanation:    "The cell has not been told which location it serves, so the deployment cannot be assigned one.",
		Remediation:    "Escalate to Datum — cell location configuration is missing.",
		Skill:          SkillPlacementTriage,
	},
	{
		Reason:         computev1alpha.WorkloadDeploymentReasonAmbiguousServingLocation,
		ConditionTypes: []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:  ActionabilityPlatform,
		Explanation: "More than one location was delivered to the cell. The cell will not guess which " +
			"it serves, so the deployment waits for the platform to resolve the conflict.",
		Remediation: remediationEscalate,
		Skill:       SkillPlacementTriage,
	},
	{
		Reason:         computev1alpha.WorkloadDeploymentReasonCityCodeMismatch,
		ConditionTypes: []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:  ActionabilityPlatform,
		Explanation:    "The deployment asked for one city and the cell serves another — it was placed on the wrong cell.",
		Remediation:    "Escalate to Datum; this is a placement fault, not a spec error.",
		Skill:          SkillPlacementTriage,
	},
	{
		Reason:         computev1alpha.WorkloadDeploymentReasonNetworkProvisioning,
		ConditionTypes: []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:  ActionabilityTransient,
		Explanation:    "The network binding or subnet is still being provisioned.",
		Remediation:    remediationWait,
	},
	{
		Reason:         computev1alpha.WorkloadDeploymentReasonInstancesProvisioning,
		ConditionTypes: []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:  ActionabilityTransient,
		Explanation:    "Instances exist but none are ready yet.",
		Remediation:    "Wait, then diagnose the individual instances if it does not settle.",
	},
	{
		Reason: computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady,
		ConditionTypes: []string{
			computev1alpha.WorkloadDeploymentAvailable,
			computev1alpha.WorkloadAvailable,
		},
		Actionability: ActionabilityUser,
		Explanation: "The worst-blocking sub-condition is a ReferencedData failure. The message carries " +
			"that sub-condition verbatim — read the ReferencedDataReady condition for the real cause.",
		Remediation: "Diagnose the ReferencedDataReady condition on the deployment or instance.",
		Skill:       SkillReferencedData,
	},
	{
		Reason: computev1alpha.WorkloadDeploymentReasonQuotaNotGranted,
		ConditionTypes: []string{
			computev1alpha.WorkloadDeploymentAvailable,
			computev1alpha.WorkloadAvailable,
		},
		Actionability: ActionabilityUser,
		Explanation: "Quota is blocking one or more instances. The real cause is the instance's " +
			"QuotaGranted condition.",
		Remediation: "Diagnose the QuotaGranted condition on the blocked instances.",
		Skill:       SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.WorkloadReasonNetworkNotFound,
		ConditionTypes: []string{computev1alpha.WorkloadAvailable},
		Actionability:  ActionabilityUser,
		Explanation:    "One or more networks referenced by the workload's network interfaces do not exist.",
		Remediation: "Create the referenced Network, or correct the network name in the workload's " +
			"network interfaces.",
	},
	{
		Reason:         computev1alpha.WorkloadReasonNoAvailablePlacements,
		ConditionTypes: []string{computev1alpha.WorkloadAvailable},
		Actionability:  ActionabilityUser,
		Explanation: "Every placement reports no available deployments. This is the last-resort default " +
			"reason — the specific cause is on the placements below.",
		Remediation: "Diagnose the individual placements and their deployments.",
		Skill:       SkillWorkloadNotAvailable,
	},
	{
		Reason:         computev1alpha.WorkloadReasonNoAvailableDeployments,
		ConditionTypes: []string{computev1alpha.WorkloadAvailable},
		Actionability:  ActionabilityUser,
		Explanation:    "No deployment in this placement is available.",
		Remediation:    "Diagnose the deployments in this placement.",
		Skill:          SkillWorkloadNotAvailable,
	},
}

var byReason = func() map[string]ReasonInfo {
	m := make(map[string]ReasonInfo, len(catalog))
	for _, r := range catalog {
		m[r.Reason] = r
	}
	return m
}()

// ExplainReason returns the catalog entry for a condition reason.
func ExplainReason(reason string) (ReasonInfo, bool) {
	info, ok := byReason[reason]
	return info, ok
}

// AllReasons returns the whole catalog, in declaration order.
func AllReasons() []ReasonInfo {
	out := make([]ReasonInfo, len(catalog))
	copy(out, catalog)
	return out
}

// pointerReasons are the reasons that redirect to a deeper condition rather
// than naming a cause. The diagnosis walk descends past these to find the
// condition they point at, and never reports one as a root cause.
var pointerReasons = map[string]struct{}{
	computev1alpha.WorkloadReasonNoAvailablePlacements:            {},
	computev1alpha.WorkloadReasonNoAvailableDeployments:           {},
	computev1alpha.WorkloadDeploymentReasonInstancesProvisioning:  {},
	computev1alpha.WorkloadDeploymentReasonQuotaNotGranted:        {},
	computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady: {},
	computev1alpha.InstanceProgrammedReasonPendingQuota:           {},
	computev1alpha.InstanceReadyReasonSchedulingGatesPresent:      {},
}

// IsPointerReason reports whether a reason merely redirects to a deeper
// condition instead of naming a cause.
func IsPointerReason(reason string) bool {
	_, ok := pointerReasons[reason]
	return ok
}
