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
	"time"

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

	// ActionabilityStalled means the reason is classified transient but the
	// condition has held it for longer than that reason's expected window.
	// Treat it as suspect and escalate.
	//
	// This is deliberately not ActionabilityPlatform. Platform means the
	// controllers reported a cause that is Datum's; stalled means nothing
	// reported a cause at all and the classification has merely been falsified
	// by elapsed time. The fault may still turn out to be the customer's
	// image, a provider that never picked the job up, or a controller that is
	// not running. Say the state has outlived its expectation and hand over
	// the object and the duration; do not assert a platform fault.
	//
	// It is never written in the catalog. It is derived at read time from a
	// condition's LastTransitionTime, so it cannot go stale in storage.
	ActionabilityStalled Actionability = "stalled"
)

// ReasonInfo explains one condition reason.
type ReasonInfo struct {
	// Reason is the condition reason as written by the controllers.
	Reason string `json:"reason"`
	// ConditionTypes are the condition types this reason is observed on.
	ConditionTypes []string `json:"conditionTypes"`
	// Actionability says who must act.
	Actionability Actionability `json:"actionability"`
	// Explanation states, in the customer's language, what actually happened.
	// The reader deploys workloads; they do not operate Datum. Write for
	// someone who knows containers, images, replicas and logs, and who has
	// never heard of a condition, a reconciler, or an allowance bucket. Anything the
	// customer writes in their own workload — image, replicas, ConfigMap, Secret,
	// placement — is their vocabulary and stays.
	// TestCopyUsesNoInternalVocabulary enforces the vocabulary half of that.
	Explanation string `json:"explanation"`
	// Remediation says what to do next. Empty for healthy reasons.
	Remediation string `json:"remediation,omitempty"`
	// Skill names the runbook covering this class of failure, if any.
	Skill string `json:"skill,omitempty"`
	// ExpectedDuration bounds how long a transient reason should plausibly
	// last. Past it, callers report ActionabilityStalled instead. Zero means
	// the reason has no window: it is not transient, or it is a resting state
	// that can legitimately persist forever (Available, QuotaDisabled).
	//
	// Omitted from JSON because time.Duration marshals as a nanosecond count,
	// which is noise in a tool result; ExpectedWithin carries it instead.
	ExpectedDuration time.Duration `json:"-"`
	// ExpectedWithin renders ExpectedDuration for a reader ("30m").
	ExpectedWithin string `json:"expectedWithin,omitempty"`
}

// Expected windows for the transient reasons.
//
// A transient classification is a claim about duration, so every reason that
// says "wait" also says how long is reasonable, and the claim becomes
// falsifiable. The tiers come from what the reasons mean rather than one global
// constant: a control-plane read is bounded by a reconcile loop, provider work
// by real infrastructure, and an aggregate reason has to outlast the children
// it is waiting on or it would be reported stalled while the instance beneath
// it was still legitimately in flight.
//
// They are generous on purpose. A window that is too short turns healthy work
// into a false alarm, which costs more trust than a stall noticed an hour late.
const (
	// windowControlPlaneRead: a controller reads or evaluates against another
	// control plane. No infrastructure provider is involved.
	windowControlPlaneRead = 5 * time.Minute

	// windowHandoff: one controller must observe another's work and act — a
	// gate clearing, a claim being picked up, resolved data reaching a cell, an
	// instance finishing a start or stop.
	windowHandoff = 10 * time.Minute

	// windowNetworkProvisioning: a provider allocates a subnet and binds it.
	// Real infrastructure, but far less of it than an instance.
	windowNetworkProvisioning = 15 * time.Minute

	// windowInstanceProvisioning: a provider creates the machine, attaches
	// disks and pulls an image. The slowest single step, and a large image pull
	// legitimately takes tens of minutes.
	windowInstanceProvisioning = 30 * time.Minute

	// windowFleetRollout: waiting on every instance underneath, so it must
	// exceed the longest instance window.
	windowFleetRollout = 45 * time.Minute
)

// Remediation strings shared by several reasons.
const (
	// remediationWait is the advice for transient states: the work is in
	// flight and nothing needs doing.
	//
	// It carries the phrase "this is normal" because that is exactly the claim
	// the stalled path has to retract; TestStalledAdviceRetractsTheWaitAdvice
	// pins that it does not survive.
	remediationWait = "Nothing to do here — this is normal and clears on its own."
	// remediationEscalate is the advice for faults on Datum's side that the
	// customer has no lever on at all.
	remediationEscalate = "Raise this with Datum. There is no change to your workload that will clear it."
)

// Skill names. These correspond to the runbooks published alongside this
// package; a binding advertises them and the assistant loads one on demand.
const (
	SkillWorkloadNotAvailable = "workload-not-available"
	SkillQuotaTriage          = "quota-triage"
	SkillInstanceNotReady     = "instance-not-ready"
	SkillReferencedData       = "referenced-data-triage"
	SkillPlacementTriage      = "placement-triage"
	SkillStalledTransient     = "stalled-transient"
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
		Explanation:    "This instance is up and serving.",
	},
	{
		Reason:         computev1alpha.WorkloadDeploymentReasonStableInstanceFound,
		ConditionTypes: []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:  ActionabilityTransient,
		Explanation:    "At least one instance here is up and serving.",
	},
	{
		Reason:         computev1alpha.InstanceProgrammedReasonProgrammed,
		ConditionTypes: []string{computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityTransient,
		Explanation:    "Datum has finished building the machine this instance runs on.",
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonQuotaAvailable,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityTransient,
		Explanation:    "This instance fits inside your project's compute quota, so it was cleared to run.",
	},
	{
		Reason:         computev1alpha.ReferencedDataReasonReady,
		ConditionTypes: []string{computev1alpha.ReferencedDataReady},
		Actionability:  ActionabilityTransient,
		Explanation:    "Every ConfigMap and Secret your workload references was found and is available to it.",
	},

	// --------------------------------------------------------------- quota
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonQuotaExceeded,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityUser,
		Explanation: "Your project asked for more compute than its quota allows, so Datum turned this " +
			"instance down. Nothing here starts until there is room for it.",
		Remediation: "Free up room or ask for more: lower the replica count, lower the CPU or memory " +
			"per instance, delete workloads you no longer need, or ask Datum to raise the project's " +
			"quota. The status message says how much was asked for and how much was left.",
		Skill: SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonNoBudget,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation: "Your project has no compute quota set up at all, so there is nothing for this " +
			"instance to draw against. That is not the same as running out (QuotaExceeded) or still " +
			"being checked (PendingEvaluation): the quota was never created, and only Datum can " +
			"create it.",
		Remediation: "Ask Datum to set up your project's compute quota. Nothing you change in the " +
			"workload will get past this.",
		Skill: SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonPendingEvaluation,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityTransient,
		Explanation:    "Datum has not finished checking this instance against your project's compute quota yet.",
		Remediation:    "Give it a few minutes. If it sits here longer than that, the quota check itself is stuck and is worth raising with Datum.",
		Skill:          SkillQuotaTriage,
		// The quota reasons keep quota-triage rather than the stall runbook:
		// its procedure already ends at "sustained PendingEvaluation is a stuck
		// quota backend", which is the more specific answer.
		ExpectedDuration: windowControlPlaneRead,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonBackendUnavailable,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation: "Datum could not reach the service that checks your project's compute quota, " +
			"so nothing can be cleared to run right now.",
		Remediation: "Raise this with Datum. Nothing in your workload affects it.",
		Skill:       SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonProjectNotFound,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation:    "Datum has no record of the project this instance belongs to, so it cannot check what that project is allowed to use.",
		Remediation:    "Raise this with Datum — the project's registration is missing or was removed. There is nothing to fix in your workload.",
		Skill:          SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonNamespaceNotFound,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation:    "Datum's record of this project is incomplete, so the check against your project's compute quota cannot run.",
		Remediation:    remediationEscalate,
		Skill:          SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonMisconfigured,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation: "Datum's quota service rejected the request outright: the rules it " +
			"checks against are missing or do not match this kind of workload.",
		Remediation: "Raise this with Datum — their quota system is misconfigured, " +
			"not in your workload.",
		Skill: SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonProjectIDUnresolvable,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityPlatform,
		Explanation: "Datum cannot tell which project this instance belongs to, so it cannot check it " +
			"against that project's compute quota.",
		Remediation: remediationEscalate,
		Skill:       SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonQuotaDisabled,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityTransient,
		Explanation:    "Compute quotas are deliberately not enforced in this environment. Nothing is wrong.",
	},
	{
		Reason:         computev1alpha.InstanceQuotaGrantedReasonValidationFailed,
		ConditionTypes: []string{computev1alpha.InstanceQuotaGranted},
		Actionability:  ActionabilityUser,
		Explanation:    "The request to check this instance against your project's compute quota was rejected as invalid before it could be checked.",
		Remediation:    "Check the CPU and memory values on the workload for anything unsupported — a zero, a negative, or a unit Datum does not accept.",
		Skill:          SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.InstanceProgrammedReasonPendingQuota,
		ConditionTypes: []string{computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityTransient,
		Explanation: "Datum is deliberately not building this instance yet: it is waiting on the check " +
			"against your project's compute quota. The real answer is in that check, not here.",
		Remediation:      "Read the instance's QuotaGranted status; this only points at it.",
		Skill:            SkillQuotaTriage,
		ExpectedDuration: windowHandoff,
	},

	// ---------------------------------------------------- instance runtime
	{
		Reason:         computev1alpha.InstanceReadyReasonImageUnavailable,
		ConditionTypes: []string{computev1alpha.InstanceReady, computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityUser,
		Explanation: "Your container image could not be downloaded, so nothing could start. Usually " +
			"that is a name or tag that does not exist, a private registry with no credentials " +
			"configured, or a registry that could not be reached.",
		Remediation: "Check the image on your workload: that the tag exists, that the repository path " +
			"is right, and — for a private registry — that pull credentials are set. The status " +
			"message usually says which of the three it was.",
		Skill: SkillInstanceNotReady,
	},
	{
		Reason:         computev1alpha.InstanceReadyReasonInstanceCrashing,
		ConditionTypes: []string{computev1alpha.InstanceReady, computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityUser,
		Explanation: "Your container starts, exits, and is restarted, over and over, so it never stays " +
			"up long enough to serve. Datum delivered and started it correctly — it is the program " +
			"inside that keeps failing.",
		Remediation: "Read the instance logs and the exit code. The usual causes are a start command " +
			"that fails immediately, a missing environment variable or mounted file, or something it " +
			"needs at startup that it cannot reach.",
		Skill: SkillInstanceNotReady,
	},
	{
		Reason:         computev1alpha.InstanceReadyReasonConfigurationError,
		ConditionTypes: []string{computev1alpha.InstanceReady, computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityUser,
		Explanation: "The machine refused to start your container because part of its configuration is " +
			"invalid — an environment variable it could not set, or a device that is not there. Your " +
			"program never ran at all.",
		Remediation: "Fix the setting the status message names; your workload was refused as written.",
		Skill:       SkillInstanceNotReady,
	},
	{
		Reason:           computev1alpha.InstanceReadyReasonProvisioning,
		ConditionTypes:   []string{computev1alpha.InstanceReady},
		Actionability:    ActionabilityTransient,
		Explanation:      "Datum is setting up the place your container runs — creating it and unpacking your image.",
		Remediation:      "Nothing to do here — this is normal, and a large image can take a while.",
		Skill:            SkillStalledTransient,
		ExpectedDuration: windowInstanceProvisioning,
	},
	{
		Reason:           computev1alpha.InstanceReadyReasonSchedulingGatesPresent,
		ConditionTypes:   []string{computev1alpha.InstanceReady},
		Actionability:    ActionabilityTransient,
		Explanation:      "This instance still has a scheduling gate on it, so it is deliberately held before it is placed anywhere. Most often the gate is Datum waiting on the quota check.",
		Remediation:      "Wait for the gate to clear. If it never clears, whatever put it there is stuck: raise it with Datum.",
		Skill:            SkillStalledTransient,
		ExpectedDuration: windowHandoff,
	},
	{
		Reason:         computev1alpha.InstanceProgrammedReasonPendingProgramming,
		ConditionTypes: []string{computev1alpha.InstanceProgrammed},
		Actionability:  ActionabilityTransient,
		Explanation:    "Datum has accepted this instance but has not started building the machine for it yet.",
		Remediation: "Give it a few minutes. If it stays here, nothing has reported back on this " +
			"instance either way: check the instance logs for a container of your own that keeps " +
			"crashing before assuming the hold-up is on Datum's side.",
		Skill:            SkillStalledTransient,
		ExpectedDuration: windowHandoff,
	},
	{
		Reason:           computev1alpha.InstanceProgrammedReasonProgrammingInProgress,
		ConditionTypes:   []string{computev1alpha.InstanceProgrammed},
		Actionability:    ActionabilityTransient,
		Explanation:      "Datum is building the machine for this instance — creating it, attaching storage, and pulling your image.",
		Remediation:      remediationWait,
		Skill:            SkillStalledTransient,
		ExpectedDuration: windowInstanceProvisioning,
	},
	{
		Reason:           computev1alpha.InstanceAvailableReasonStarting,
		ConditionTypes:   []string{computev1alpha.InstanceAvailable},
		Actionability:    ActionabilityTransient,
		Explanation:      "The instance is booting.",
		Remediation:      remediationWait,
		Skill:            SkillStalledTransient,
		ExpectedDuration: windowHandoff,
	},
	{
		Reason:           computev1alpha.InstanceAvailableReasonStopping,
		ConditionTypes:   []string{computev1alpha.InstanceAvailable},
		Actionability:    ActionabilityTransient,
		Explanation:      "The instance is shutting down.",
		Remediation:      remediationWait,
		Skill:            SkillStalledTransient,
		ExpectedDuration: windowHandoff,
	},
	{
		Reason:         computev1alpha.InstanceAvailableReasonStopped,
		ConditionTypes: []string{computev1alpha.InstanceAvailable},
		Actionability:  ActionabilityUser,
		Explanation:    "This instance is stopped, so it is not serving.",
		Remediation:    "Start it again — or check whether someone stopped it on purpose, because a deliberate stop looks exactly like this.",
	},
	{
		Reason:         computev1alpha.InstanceReadyReasonSuspended,
		ConditionTypes: []string{computev1alpha.InstanceReady, computev1alpha.InstanceAvailable},
		Actionability:  ActionabilityPlatform,
		Explanation: "Your project is suspended, so Datum stopped this instance. Nothing has been " +
			"thrown away — where it runs, its disk, and its share of your project's quota are all held, and " +
			"it starts back up from the same disk once the suspension is lifted.",
		Remediation: "Clear the suspension — it is usually billing or an account hold. No change to " +
			"the workload will start it while the project is suspended.",
	},

	// --------------------------------------------------- referenced data
	{
		Reason:         computev1alpha.ReferencedDataReasonSourceNotFound,
		ConditionTypes: []string{computev1alpha.ReferencedDataReady},
		Actionability:  ActionabilityUser,
		Explanation: "Your workload references a ConfigMap or Secret that does not exist in this " +
			"project, so its instances cannot start without it. The status message names the " +
			"missing one.",
		Remediation: "Create the missing ConfigMap or Secret in this project, or fix the name your " +
			"workload references it by.",
		Skill: SkillReferencedData,
	},
	{
		Reason:         computev1alpha.ReferencedDataReasonSourceUnauthorized,
		ConditionTypes: []string{computev1alpha.ReferencedDataReady},
		Actionability:  ActionabilityPlatform,
		Explanation: "The ConfigMap or Secret exists, but Datum does not have permission to read it, " +
			"so it cannot reach your instances.",
		Remediation: "Raise this with Datum — their read permissions are too narrow. Do not recreate " +
			"anything; your ConfigMap or Secret is fine as it is.",
		Skill: SkillReferencedData,
	},
	{
		Reason:         computev1alpha.ReferencedDataReasonSourceTooLarge,
		ConditionTypes: []string{computev1alpha.ReferencedDataReady},
		Actionability:  ActionabilityUser,
		Explanation:    "A ConfigMap or Secret your workload references is larger than Datum allows.",
		Remediation:    "Shrink it, or split it into several smaller ones.",
		Skill:          SkillReferencedData,
	},
	{
		Reason:           computev1alpha.ReferencedDataReasonResolving,
		ConditionTypes:   []string{computev1alpha.ReferencedDataReady},
		Actionability:    ActionabilityTransient,
		Explanation:      "Datum is reading the ConfigMaps and Secrets your workload references.",
		Remediation:      remediationWait,
		Skill:            SkillStalledTransient,
		ExpectedDuration: windowControlPlaneRead,
	},
	{
		Reason:           computev1alpha.ReferencedDataReasonAwaitingPropagation,
		ConditionTypes:   []string{computev1alpha.ReferencedDataReady},
		Actionability:    ActionabilityTransient,
		Explanation:      "The ConfigMaps and Secrets have been read and are on their way to the machine that will run your instance.",
		Remediation:      remediationWait,
		Skill:            SkillStalledTransient,
		ExpectedDuration: windowHandoff,
	},

	// ------------------------------------------- placement / deployment
	{
		Reason:         computev1alpha.WorkloadDeploymentReasonNoMatchingLocation,
		ConditionTypes: []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:  ActionabilityPlatform,
		Explanation:    "Datum has not finished setting up the location this part of your workload was sent to, so it has nowhere to run.",
		Remediation:    "Raise this with Datum — the location setup is missing on their side. Nothing in your workload will change it.",
		Skill:          SkillPlacementTriage,
	},
	{
		Reason:         computev1alpha.WorkloadDeploymentReasonAmbiguousServingLocation,
		ConditionTypes: []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:  ActionabilityPlatform,
		Explanation: "Datum's setup for this location contradicts itself — it has been given more than " +
			"one identity — so your workload is being held rather than started somewhere it may not " +
			"belong.",
		Remediation: remediationEscalate,
		Skill:       SkillPlacementTriage,
	},
	{
		Reason:         computev1alpha.WorkloadDeploymentReasonCityCodeMismatch,
		ConditionTypes: []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:  ActionabilityPlatform,
		Explanation:    "Your workload asked to run in one city and was sent to another, so Datum is refusing to start it in the wrong place.",
		Remediation:    "Raise this with Datum — your placement request is fine; it was routed to the wrong place on their side.",
		Skill:          SkillPlacementTriage,
	},
	{
		Reason:           computev1alpha.WorkloadDeploymentReasonNetworkProvisioning,
		ConditionTypes:   []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:    ActionabilityTransient,
		Explanation:      "Datum is still setting up the network this workload attaches to.",
		Remediation:      remediationWait,
		Skill:            SkillStalledTransient,
		ExpectedDuration: windowNetworkProvisioning,
	},
	{
		Reason:           computev1alpha.WorkloadDeploymentReasonInstancesProvisioning,
		ConditionTypes:   []string{computev1alpha.WorkloadDeploymentAvailable},
		Actionability:    ActionabilityTransient,
		Explanation:      "The instances exist, but none of them is serving yet.",
		Remediation:      "Give it time; if it does not settle, look at the instances one by one — each reports its own cause.",
		Skill:            SkillStalledTransient,
		ExpectedDuration: windowFleetRollout,
	},
	{
		Reason: computev1alpha.WorkloadDeploymentReasonReferencedDataNotReady,
		ConditionTypes: []string{
			computev1alpha.WorkloadDeploymentAvailable,
			computev1alpha.WorkloadAvailable,
		},
		Actionability: ActionabilityUser,
		Explanation: "A ConfigMap or Secret your workload references is not ready. The status message " +
			"repeats the underlying problem word for word; that is the one to answer.",
		Remediation: "Read the ReferencedDataReady status on the deployment or the instance for the real cause.",
		Skill:       SkillReferencedData,
	},
	{
		Reason: computev1alpha.WorkloadDeploymentReasonQuotaNotGranted,
		ConditionTypes: []string{
			computev1alpha.WorkloadDeploymentAvailable,
			computev1alpha.WorkloadAvailable,
		},
		Actionability: ActionabilityUser,
		Explanation: "Your project's compute quota is holding back one or more instances. The real answer " +
			"is in the per-instance quota check, not here.",
		Remediation: "Read the QuotaGranted status on the blocked instances.",
		Skill:       SkillQuotaTriage,
	},
	{
		Reason:         computev1alpha.WorkloadReasonNetworkNotFound,
		ConditionTypes: []string{computev1alpha.WorkloadAvailable},
		Actionability:  ActionabilityUser,
		Explanation:    "Your workload attaches to a network that does not exist in this project.",
		Remediation: "Create that network, or fix the network name on the workload's network " +
			"interfaces.",
	},
	{
		Reason:         computev1alpha.WorkloadReasonNoAvailablePlacements,
		ConditionTypes: []string{computev1alpha.WorkloadAvailable},
		Actionability:  ActionabilityUser,
		Explanation: "Nothing is running in any of this workload's placements. This is the fallback " +
			"answer when nothing more specific was found at the top — the real cause is in one of the " +
			"placements underneath.",
		Remediation: "Look at each placement in turn; the specific cause is on one of them.",
		Skill:       SkillWorkloadNotAvailable,
	},
	{
		Reason:         computev1alpha.WorkloadReasonNoAvailableDeployments,
		ConditionTypes: []string{computev1alpha.WorkloadAvailable},
		Actionability:  ActionabilityUser,
		Explanation:    "Nothing in this placement is serving yet.",
		Remediation:    "Look at what this placement created; the cause is there.",
		Skill:          SkillWorkloadNotAvailable,
	},
}

// byReason indexes the catalog and, in the same pass, renders each entry's
// window once so the duration is written in exactly one place — the table —
// and every reader sees the same string.
var byReason = func() map[string]ReasonInfo {
	m := make(map[string]ReasonInfo, len(catalog))
	for i := range catalog {
		catalog[i].ExpectedWithin = humanDuration(catalog[i].ExpectedDuration)
		m[catalog[i].Reason] = catalog[i]
	}
	return m
}()

// ActionabilityAt reports how a reason should be treated given how long its
// condition has held, escalating a transient reason to ActionabilityStalled
// once it has outlived the window the catalog claims for it.
//
// lastTransition is the condition's LastTransitionTime in RFC 3339, and the
// comparison is made against now on every read — nothing about a stall is
// persisted or inferred from anything but elapsed time.
//
// A missing, unparseable, or implausible timestamp returns the static
// classification: absence of evidence that a state is old is not evidence that
// it has stalled. The object's own creationTimestamp deliberately does not
// escalate here either — an object can be nine days old and have failed a
// minute ago, so age alone would manufacture false alarms.
func ActionabilityAt(info ReasonInfo, lastTransition string, now time.Time) Actionability {
	if info.Actionability != ActionabilityTransient || info.ExpectedDuration <= 0 {
		return info.Actionability
	}
	elapsed, ok := age(lastTransition, now)
	if !ok || elapsed <= info.ExpectedDuration {
		return info.Actionability
	}
	return ActionabilityStalled
}

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
