package agent

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// The tools compute publishes to an assistant. All five are read-only.
//
// There is deliberately no mutating tool here — no workload_delete, no scale,
// no restart. Compute does not need to ship a destructive tool to prove the
// allow-list works: enforcement is the gateway's job (an MCPRoute toolSelector
// naming exactly the sanctioned tools), and a provider that simply never
// implements one cannot have it called through any path. If a mutating tool is
// ever wanted, it needs its own review, not a quiet addition here.
const (
	ToolWorkloadsList    = "workloads_list"
	ToolWorkloadsGet     = "workloads_get"
	ToolInstancesList    = "instances_list"
	ToolWorkloadDiagnose = "workload_diagnose"
	ToolReasonExplain    = "reason_explain"
)

// ToolDeps is what one request's tool calls operate over: where to read from,
// and which project's namespace they are confined to.
type ToolDeps struct {
	Reader    Reader
	Namespace string
}

// DepsFor resolves the dependencies for a tool call.
//
// It is a function rather than a plain value so the caller decides how the
// identity and project behind a call are established — the server derives both
// from the incoming HTTP request, while tests supply them directly.
type DepsFor func(context.Context) (ToolDeps, error)

// ---------------------------------------------------------------- I/O types

// ConditionView is the part of a condition worth spending tokens on.
type ConditionView struct {
	Type    string `json:"type"`
	Status  string `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}

// PlacementView summarises one of a workload's placements.
type PlacementView struct {
	Name            string          `json:"name"`
	Replicas        int32           `json:"replicas"`
	ReadyReplicas   int32           `json:"readyReplicas"`
	DesiredReplicas int32           `json:"desiredReplicas"`
	Conditions      []ConditionView `json:"conditions,omitempty"`
}

// WorkloadView is a Workload's identity and status, without its spec.
type WorkloadView struct {
	Name            string          `json:"name"`
	Namespace       string          `json:"namespace"`
	Replicas        int32           `json:"replicas"`
	ReadyReplicas   int32           `json:"readyReplicas"`
	DesiredReplicas int32           `json:"desiredReplicas"`
	Conditions      []ConditionView `json:"conditions,omitempty"`
	Placements      []PlacementView `json:"placements,omitempty"`
}

// DeploymentView is a WorkloadDeployment's placement and status.
type DeploymentView struct {
	Name          string          `json:"name"`
	Placement     string          `json:"placement,omitempty"`
	CityCode      string          `json:"cityCode,omitempty"`
	Location      string          `json:"location,omitempty"`
	ReadyReplicas int32           `json:"readyReplicas"`
	Conditions    []ConditionView `json:"conditions,omitempty"`
}

// InstanceView is an Instance's identity and conditions. The deployment,
// placement and city come from the labels the controllers stamp on every
// instance, so no extra lookup is needed to say where one lives.
type InstanceView struct {
	Name       string          `json:"name"`
	Deployment string          `json:"deployment,omitempty"`
	Placement  string          `json:"placement,omitempty"`
	CityCode   string          `json:"cityCode,omitempty"`
	Conditions []ConditionView `json:"conditions,omitempty"`
}

// WorkloadSummary is one row of the fleet view.
type WorkloadSummary struct {
	Workload string `json:"workload"`
	// Available reports the Workload's Available condition.
	Available bool `json:"available"`
	// ReadyReplicas is rendered "ready/total" so a partially serving workload
	// reads at a glance.
	ReadyReplicas string `json:"readyReplicas"`
	// RootCauseReason and Actionability are empty for a healthy workload.
	RootCauseReason string        `json:"rootCauseReason,omitempty"`
	Actionability   Actionability `json:"actionability,omitempty"`
	// RootCauseSince is when the root-cause condition last transitioned, in
	// RFC 3339. A string rather than a metav1.Time for the reason
	// Cause.LastTransitionTime gives: this struct is a tool output schema.
	RootCauseSince string `json:"rootCauseSince,omitempty"`
	// RootCauseFor is that same age rendered "5d" / "12m". Both are emitted
	// because the consumer is a language model: the timestamp is the fact, and
	// the relative form is what makes a five-day "in progress" impossible to
	// read as normal in-flight work without doing arithmetic first.
	RootCauseFor string `json:"rootCauseFor,omitempty"`
	// RootCauseFailingFor is the floor on how long the root-cause object has
	// been failing, from its creationTimestamp. RootCauseFor alone measures
	// condition churn: a restart rewrites lastTransitionTime, so a workload
	// broken for nine days can report nine hours. When the two disagree, the
	// gap is the finding.
	RootCauseFailingFor string `json:"rootCauseFailingFor,omitempty"`
	// RootCausePattern names a failure shape derived from elapsed evidence
	// rather than reported by a controller. Empty unless one was recognised.
	RootCausePattern string `json:"rootCausePattern,omitempty"`
}

// WorkloadsListInput takes no arguments: the project is fixed by the request,
// never chosen by the model.
type WorkloadsListInput struct{}

// WorkloadsListOutput is the fleet view, worst first.
type WorkloadsListOutput struct {
	Workloads []WorkloadSummary `json:"workloads"`
}

// WorkloadsGetInput names one workload.
type WorkloadsGetInput struct {
	Name string `json:"name" jsonschema:"Workload name, e.g. \"api-backend\""`
}

// WorkloadsGetOutput is the raw condition tree for one workload.
type WorkloadsGetOutput struct {
	Workload    WorkloadView     `json:"workload"`
	Deployments []DeploymentView `json:"deployments"`
	Instances   []InstanceView   `json:"instances"`
}

// InstancesListInput optionally narrows to one workload.
type InstancesListInput struct {
	Workload string `json:"workload,omitempty" jsonschema:"Optional workload name to filter by, e.g. \"api-backend\""`
}

// InstancesListOutput is the per-instance condition view.
type InstancesListOutput struct {
	Instances []InstanceView `json:"instances"`
}

// WorkloadDiagnoseInput names the workload to diagnose.
type WorkloadDiagnoseInput struct {
	Name string `json:"name" jsonschema:"Workload name, e.g. \"api-backend\""`
}

// ReasonExplainInput optionally names one reason.
type ReasonExplainInput struct {
	Reason string `json:"reason,omitempty" jsonschema:"Reason string, e.g. \"QuotaExceeded\". Omit to list every known reason."`
}

// ReasonExplainOutput carries one reason, or the whole catalog when no reason
// was named.
type ReasonExplainOutput struct {
	Reason  *ReasonInfo  `json:"reason,omitempty"`
	Reasons []ReasonInfo `json:"reasons,omitempty"`
}

// ------------------------------------------------------------ registration

// RegisterTools adds compute's read-only diagnostic tools to s.
//
// deps is consulted per call rather than captured once, so a single server can
// serve many callers without any of them inheriting another's identity or
// project.
func RegisterTools(s *mcp.Server, deps DepsFor) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  ToolWorkloadsList,
		Title: "List workloads",
		Description: "List every Workload in the project with its availability, ready/desired replicas, " +
			"and — when it is not fully available — the root-cause reason, how long it has held that " +
			"state (rootCauseFor, e.g. \"5d\"), how long the failing object has existed " +
			"(rootCauseFailingFor — a restart rewrites the condition clock, so trust the larger " +
			"number), and whether that cause is user-actionable, a platform fault, transient, or " +
			"stalled. Start here. Read-only.",
	}, workloadsList(deps))

	mcp.AddTool(s, &mcp.Tool{
		Name:  ToolWorkloadsGet,
		Title: "Get workload detail",
		Description: "Get one Workload by name with its full condition tree: workload conditions, its " +
			"placements, every WorkloadDeployment, and every Instance with their conditions. Use when " +
			"you need the raw status rather than a diagnosis. Read-only.",
	}, workloadsGet(deps))

	mcp.AddTool(s, &mcp.Tool{
		Name:  ToolInstancesList,
		Title: "List instances",
		Description: "List Instances, optionally filtered to one workload, with each instance's conditions " +
			"(Ready, Available, Programmed, QuotaGranted, ReferencedDataReady). Use to see how a failure " +
			"is distributed across replicas. Read-only.",
	}, instancesList(deps))

	mcp.AddTool(s, &mcp.Tool{
		Name:  ToolWorkloadDiagnose,
		Title: "Diagnose workload",
		Description: "Diagnose why a Workload is not available. Walks Workload -> WorkloadDeployment -> " +
			"Instance, follows compute's pointer reasons (QuotaNotGranted, NoAvailablePlacements, " +
			"ReferencedDataNotReady) down to the condition that names the real cause, and returns that " +
			"root cause with an explanation, how long it has held that state, whether it is " +
			"user-actionable, a platform fault, transient, or stalled (transient for longer than that " +
			"reason should take), concrete next steps, and which skill covers the full procedure. " +
			"Read inStateFor against failingFor: the first is how long the reason has held, the " +
			"second a floor on how long the object has been broken, and a large gap means the " +
			"condition is being rewritten. A reported=false cause means no controller reported " +
			"success or failure — do not repeat its reason as an observation. This is the tool to reach for when " +
			"someone asks why a workload is broken. Read-only.",
	}, workloadDiagnose(deps))

	mcp.AddTool(s, &mcp.Tool{
		Name:  ToolReasonExplain,
		Title: "Explain a condition reason",
		Description: "Explain any compute condition reason (e.g. \"QuotaExceeded\", \"ImageUnavailable\", " +
			"\"CityCodeMismatch\"): what it means, which condition types carry it, whether it is " +
			"user-actionable, a platform fault, or transient, how long a transient one should take " +
			"(expectedWithin), and how to remediate it. Call with no " +
			"argument to list the whole catalog. Use when you encounter a reason on a resource the " +
			"diagnose tool did not cover. Read-only.",
	}, reasonExplain(deps))
}

// ---------------------------------------------------------------- handlers

func workloadsList(deps DepsFor) mcp.ToolHandlerFor[WorkloadsListInput, WorkloadsListOutput] {
	return func(
		ctx context.Context, _ *mcp.CallToolRequest, _ WorkloadsListInput,
	) (*mcp.CallToolResult, WorkloadsListOutput, error) {
		d, err := deps(ctx)
		if err != nil {
			return nil, WorkloadsListOutput{}, err
		}

		workloads, err := d.Reader.ListWorkloads(ctx, d.Namespace)
		if err != nil {
			return nil, WorkloadsListOutput{}, err
		}

		// One clock for the whole listing, so every row's age is measured from
		// the same instant and the rows are comparable to each other.
		now := time.Now()

		out := WorkloadsListOutput{Workloads: make([]WorkloadSummary, 0, len(workloads))}
		for i := range workloads {
			w := &workloads[i]
			diagnosis, err := diagnoseOne(ctx, d, w, now)
			if err != nil {
				return nil, WorkloadsListOutput{}, err
			}
			summary := WorkloadSummary{
				Workload:      w.Name,
				Available:     diagnosis.Available,
				ReadyReplicas: fmt.Sprintf("%d/%d", diagnosis.Instances.Ready, diagnosis.Instances.Total),
			}
			if diagnosis.RootCause != nil {
				summary.RootCauseReason = diagnosis.RootCause.Reason
				summary.Actionability = diagnosis.RootCause.Actionability
				summary.RootCauseSince = diagnosis.RootCause.LastTransitionTime
				summary.RootCauseFor = diagnosis.RootCause.InStateFor
				summary.RootCauseFailingFor = diagnosis.RootCause.FailingFor
				summary.RootCausePattern = diagnosis.RootCause.Pattern
			}
			out.Workloads = append(out.Workloads, summary)
		}

		// Worst first: an operator asking "what is broken" should not have to
		// scan past healthy workloads.
		sortUnavailableFirst(out.Workloads)
		return nil, out, nil
	}
}

func workloadsGet(deps DepsFor) mcp.ToolHandlerFor[WorkloadsGetInput, WorkloadsGetOutput] {
	return func(
		ctx context.Context, _ *mcp.CallToolRequest, in WorkloadsGetInput,
	) (*mcp.CallToolResult, WorkloadsGetOutput, error) {
		d, err := deps(ctx)
		if err != nil {
			return nil, WorkloadsGetOutput{}, err
		}
		if in.Name == "" {
			return nil, WorkloadsGetOutput{}, fmt.Errorf("name is required")
		}

		workload, err := d.Reader.GetWorkload(ctx, d.Namespace, in.Name)
		if err != nil {
			return nil, WorkloadsGetOutput{}, err
		}
		deployments, err := d.Reader.ListDeployments(ctx, d.Namespace, in.Name)
		if err != nil {
			return nil, WorkloadsGetOutput{}, err
		}
		instances, err := d.Reader.ListInstances(ctx, d.Namespace, in.Name)
		if err != nil {
			return nil, WorkloadsGetOutput{}, err
		}

		return nil, WorkloadsGetOutput{
			Workload:    toWorkloadView(workload),
			Deployments: toDeploymentViews(deployments),
			Instances:   toInstanceViews(instances),
		}, nil
	}
}

func instancesList(deps DepsFor) mcp.ToolHandlerFor[InstancesListInput, InstancesListOutput] {
	return func(
		ctx context.Context, _ *mcp.CallToolRequest, in InstancesListInput,
	) (*mcp.CallToolResult, InstancesListOutput, error) {
		d, err := deps(ctx)
		if err != nil {
			return nil, InstancesListOutput{}, err
		}

		if in.Workload != "" {
			instances, err := d.Reader.ListInstances(ctx, d.Namespace, in.Workload)
			if err != nil {
				return nil, InstancesListOutput{}, err
			}
			return nil, InstancesListOutput{Instances: toInstanceViews(instances)}, nil
		}

		workloads, err := d.Reader.ListWorkloads(ctx, d.Namespace)
		if err != nil {
			return nil, InstancesListOutput{}, err
		}
		var all []computev1alpha.Instance
		for i := range workloads {
			instances, err := d.Reader.ListInstances(ctx, d.Namespace, workloads[i].Name)
			if err != nil {
				return nil, InstancesListOutput{}, err
			}
			all = append(all, instances...)
		}
		return nil, InstancesListOutput{Instances: toInstanceViews(all)}, nil
	}
}

func workloadDiagnose(deps DepsFor) mcp.ToolHandlerFor[WorkloadDiagnoseInput, Diagnosis] {
	return func(
		ctx context.Context, _ *mcp.CallToolRequest, in WorkloadDiagnoseInput,
	) (*mcp.CallToolResult, Diagnosis, error) {
		d, err := deps(ctx)
		if err != nil {
			return nil, Diagnosis{}, err
		}
		if in.Name == "" {
			return nil, Diagnosis{}, fmt.Errorf("name is required")
		}

		workload, err := d.Reader.GetWorkload(ctx, d.Namespace, in.Name)
		if err != nil {
			return nil, Diagnosis{}, err
		}
		diagnosis, err := diagnoseOne(ctx, d, workload, time.Now())
		if err != nil {
			return nil, Diagnosis{}, err
		}
		return nil, diagnosis, nil
	}
}

// reasonExplain reads only the catalog, but still resolves deps so that an
// unauthenticated caller cannot use it to probe the server.
func reasonExplain(deps DepsFor) mcp.ToolHandlerFor[ReasonExplainInput, ReasonExplainOutput] {
	return func(
		ctx context.Context, _ *mcp.CallToolRequest, in ReasonExplainInput,
	) (*mcp.CallToolResult, ReasonExplainOutput, error) {
		if _, err := deps(ctx); err != nil {
			return nil, ReasonExplainOutput{}, err
		}

		if in.Reason == "" {
			return nil, ReasonExplainOutput{Reasons: AllReasons()}, nil
		}
		info, ok := ExplainReason(in.Reason)
		if !ok {
			return nil, ReasonExplainOutput{}, fmt.Errorf(
				"no catalog entry for reason %q; call %s with no argument to list every known reason",
				in.Reason, ToolReasonExplain)
		}
		return nil, ReasonExplainOutput{Reason: &info}, nil
	}
}

// ----------------------------------------------------------------- helpers

func diagnoseOne(
	ctx context.Context, d ToolDeps, w *computev1alpha.Workload, now time.Time,
) (Diagnosis, error) {
	deployments, err := d.Reader.ListDeployments(ctx, d.Namespace, w.Name)
	if err != nil {
		return Diagnosis{}, err
	}
	instances, err := d.Reader.ListInstances(ctx, d.Namespace, w.Name)
	if err != nil {
		return Diagnosis{}, err
	}
	return DiagnoseAt(now, w, deployments, instances), nil
}

// sortUnavailableFirst puts unavailable workloads at the top. The sort is
// stable so workloads in the same state keep the reader's order, which keeps
// output reproducible between calls.
func sortUnavailableFirst(rows []WorkloadSummary) {
	sort.SliceStable(rows, func(i, j int) bool {
		return !rows[i].Available && rows[j].Available
	})
}

func toConditionViews(conditions []metav1.Condition) []ConditionView {
	out := make([]ConditionView, 0, len(conditions))
	for _, c := range conditions {
		out = append(out, ConditionView{
			Type:    c.Type,
			Status:  string(c.Status),
			Reason:  c.Reason,
			Message: c.Message,
		})
	}
	return out
}

func toWorkloadView(w *computev1alpha.Workload) WorkloadView {
	view := WorkloadView{
		Name:            w.Name,
		Namespace:       w.Namespace,
		Replicas:        w.Status.Replicas,
		ReadyReplicas:   w.Status.ReadyReplicas,
		DesiredReplicas: w.Status.DesiredReplicas,
		Conditions:      toConditionViews(w.Status.Conditions),
		Placements:      make([]PlacementView, 0, len(w.Status.Placements)),
	}
	for _, p := range w.Status.Placements {
		view.Placements = append(view.Placements, PlacementView{
			Name:            p.Name,
			Replicas:        p.Replicas,
			ReadyReplicas:   p.ReadyReplicas,
			DesiredReplicas: p.DesiredReplicas,
			Conditions:      toConditionViews(p.Conditions),
		})
	}
	return view
}

func toDeploymentViews(deployments []computev1alpha.WorkloadDeployment) []DeploymentView {
	out := make([]DeploymentView, 0, len(deployments))
	for i := range deployments {
		d := &deployments[i]
		view := DeploymentView{
			Name:          d.Name,
			Placement:     d.Spec.PlacementName,
			CityCode:      d.Spec.CityCode,
			ReadyReplicas: d.Status.ReadyReplicas,
			Conditions:    toConditionViews(d.Status.Conditions),
		}
		if d.Status.Location != nil {
			view.Location = d.Status.Location.Name
		}
		out = append(out, view)
	}
	return out
}

func toInstanceViews(instances []computev1alpha.Instance) []InstanceView {
	out := make([]InstanceView, 0, len(instances))
	for i := range instances {
		inst := &instances[i]
		out = append(out, InstanceView{
			Name:       inst.Name,
			Deployment: inst.Labels[computev1alpha.WorkloadDeploymentNameLabel],
			Placement:  inst.Labels[computev1alpha.PlacementNameLabel],
			CityCode:   inst.Labels[computev1alpha.CityCodeLabel],
			Conditions: toConditionViews(inst.Status.Conditions),
		})
	}
	return out
}
