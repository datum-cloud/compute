// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"go.datum.net/compute/internal/workloadspec"
)

// ToolWorkloadRender turns a short description of a deployment into a complete
// Workload manifest.
//
// Compute publishes no tool that writes. The assistant's own base tools
// validate, plan and apply manifests of any kind as the caller, behind a plan
// token and an explicit confirmation. What they cannot know is how to write a
// Workload the admission webhook accepts, and a model guessing at that invents
// fields plausibly. Rendering is the compute-specific half, so it is the half
// compute publishes.
const ToolWorkloadRender = "compute_workload_render"

// The assistant's base tools the render output points at. They belong to the
// assistant, not to this server, so nothing here can check they exist; the
// names are the assistant's published contract.
const (
	baseToolLocationsList = "locations_list"
	baseToolResourcesList = "resources_list"
	baseToolResourcesPlan = "resources_plan"
)

// ---------------------------------------------------------------- I/O types

// RenderPlacement is one group of locations scaled together.
type RenderPlacement struct {
	Name string `json:"name,omitempty" jsonschema:"Placement name, a DNS label. Defaults to \"default\"."`
	// Locations and LocationSelector are the two ways to say where a placement
	// runs, and exactly one of them must be given. The schema says so rather
	// than leaving a model to discover it from a rejection.
	Locations        []string                `json:"locations,omitempty" jsonschema:"Location names this placement runs in, e.g. [\"us-south-dfw-1\"]. Take the names verbatim from locations_list with service \"compute\" — a name that is not in that list can never be satisfied. Set exactly one of locations or locationSelector."`
	LocationSelector *RenderLocationSelector `json:"locationSelector,omitempty" jsonschema:"Place at every location whose topology matches, instead of naming them. Use this for \"every location in Dallas\" or \"every location in a region\": match on the topology keys locations_list reports, such as topology.datum.net/city-code. New locations matching it are picked up automatically. Set exactly one of locations or locationSelector."`
	MinReplicas      int32                   `json:"minReplicas,omitempty" jsonschema:"Instances to run per placement. At least 1 — there is no scaling to zero — and at most 1000. Defaults to 1."`
}

// RenderLocationSelector is a label selector over location topology, in the
// two forms the API accepts. An empty selector is refused rather than read as
// matching every location.
type RenderLocationSelector struct {
	MatchLabels      map[string]string           `json:"matchLabels,omitempty" jsonschema:"Topology key/value pairs a location must carry, e.g. {\"topology.datum.net/city-code\": \"DFW\"}."`
	MatchExpressions []RenderLocationSelectorReq `json:"matchExpressions,omitempty" jsonschema:"Set-based requirements over topology keys, for cases matchLabels cannot express, such as one of several cities."`
}

// RenderLocationSelectorReq is one set-based requirement.
type RenderLocationSelectorReq struct {
	Key      string   `json:"key" jsonschema:"Topology key, e.g. topology.datum.net/city-code."`
	Operator string   `json:"operator" jsonschema:"In, NotIn, Exists or DoesNotExist."`
	Values   []string `json:"values,omitempty" jsonschema:"Values for In and NotIn. Must be empty for Exists and DoesNotExist."`
}

// RenderPort is a named port the workload serves.
type RenderPort struct {
	Name     string `json:"name" jsonschema:"Port name, e.g. \"http\". At most 15 characters, and must contain a letter."`
	Port     int32  `json:"port" jsonschema:"Port number, 1 to 65535."`
	Protocol string `json:"protocol,omitempty" jsonschema:"TCP, UDP or SCTP. Defaults to TCP."`
}

// RenderKeyRef selects one key of a ConfigMap or Secret.
type RenderKeyRef struct {
	Name string `json:"name" jsonschema:"Name of the ConfigMap or Secret, which must already exist in the project."`
	Key  string `json:"key" jsonschema:"Key within it."`
}

// RenderEnvVar is one environment variable on the container.
type RenderEnvVar struct {
	Name            string        `json:"name" jsonschema:"Variable name."`
	Value           string        `json:"value,omitempty" jsonschema:"Literal value. Set at most one of value, configMapKeyRef, secretKeyRef."`
	ConfigMapKeyRef *RenderKeyRef `json:"configMapKeyRef,omitempty" jsonschema:"Read the value from a ConfigMap key instead."`
	SecretKeyRef    *RenderKeyRef `json:"secretKeyRef,omitempty" jsonschema:"Read the value from a Secret key instead."`
}

// RenderMount projects a ConfigMap or Secret into the instance's filesystem.
type RenderMount struct {
	Name      string `json:"name,omitempty" jsonschema:"Volume name. Defaults to the ConfigMap or Secret name."`
	ConfigMap string `json:"configMap,omitempty" jsonschema:"Name of the ConfigMap to mount. Set exactly one of configMap or secret."`
	Secret    string `json:"secret,omitempty" jsonschema:"Name of the Secret to mount. Set exactly one of configMap or secret."`
	MountPath string `json:"mountPath" jsonschema:"Absolute path the contents appear at inside the instance."`
}

// RenderVM asks for a virtual machine rather than a container.
type RenderVM struct {
	SSHKeys   []string `json:"sshKeys" jsonschema:"Keys authorized to log in, each \"username:ssh-public-key\". At least one — a machine with no key is unreachable and is rejected."`
	BootImage string   `json:"bootImage,omitempty" jsonschema:"Disk image the machine boots. Defaults to datumcloud/ubuntu-2204-lts, currently the only one accepted."`
}

// WorkloadRenderInput is the flat description a manifest is rendered from. It
// mirrors workloadspec.Input field for field, so the tool schema can be worded
// for a model without that wording leaking into the renderer.
type WorkloadRenderInput struct {
	Name         string            `json:"name" jsonschema:"Workload name, a DNS label, e.g. \"api-backend\". Cannot be changed later."`
	Image        string            `json:"image,omitempty" jsonschema:"Fully qualified container image, e.g. \"ghcr.io/acme/api:1.4.2\". Required unless vm is set. A bare name is the most common cause of ImageUnavailable afterwards."`
	InstanceType string            `json:"instanceType,omitempty" jsonschema:"Instance type from compute_instance_types_list. Defaults to the only one accepted today."`
	RuntimeClass string            `json:"runtimeClass,omitempty" jsonschema:"Execution tier the instances run in, named verbatim from the RuntimeClass objects resources_list returns for compute.datumapis.com/v1alpha. Leave unset unless the person named one: the server picks its default, and the tier cannot be changed after the workload exists."`
	Network      string            `json:"network,omitempty" jsonschema:"Network the instance attaches to. Defaults to \"default\"."`
	Placements   []RenderPlacement `json:"placements" jsonschema:"Where instances run and how many. At least one is required."`
	Ports        []RenderPort      `json:"ports,omitempty" jsonschema:"Named ports the workload serves. Each is also opened to the internet, since a port nothing can reach is not useful."`
	Env          []RenderEnvVar    `json:"env,omitempty" jsonschema:"Environment variables on the container. Not accepted for a virtual machine."`
	ConfigMounts []RenderMount     `json:"configMounts,omitempty" jsonschema:"ConfigMaps and Secrets projected into the instance's filesystem."`
	PublicIPv4   bool              `json:"publicIPv4,omitempty" jsonschema:"Ask for a public IPv4 address. Settled at create: it cannot be added or removed later, so ask before rendering rather than defaulting it."`
	Labels       map[string]string `json:"labels,omitempty" jsonschema:"Labels applied to the workload and to every instance it creates."`
	VM           *RenderVM         `json:"vm,omitempty" jsonschema:"Render a virtual machine instead of a container. Only when the person needs a whole operating system to log into."`
}

// WorkloadRenderOutput is the manifest and what rendering it settled.
type WorkloadRenderOutput struct {
	// Manifest is the complete Workload, as YAML.
	Manifest string `json:"manifest"`
	// Notes are the decisions this manifest fixes for the life of the workload
	// and the defaults that were filled in. Worth reading out: several of them
	// cannot be changed after the first apply.
	Notes []string `json:"notes,omitempty"`
}

// ------------------------------------------------------------ registration

// registerRenderTool adds compute_workload_render to s.
func registerRenderTool(s *mcp.Server, deps DepsFor) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  ToolWorkloadRender,
		Title: "Render a workload manifest",
		Description: "Turn a short description of a deployment — name, image, where, how many — into a " +
			"complete Workload manifest, and report what rendering it settled. Nothing is read and nothing " +
			"is changed, so render as often as it takes to get the manifest right. Read the manifest that " +
			"comes back rather than assuming it says what was asked for, and read the notes: they name the " +
			"choices that cannot be changed once the workload exists, the interface's address families and " +
			"a public IPv4 address among them. Gather the inputs from the person rather than inventing " +
			"them: take location names from locations_list with service \"compute\" and the instance " +
			"type from compute_instance_types_list. A placement either names locations or selects them by " +
			"topology; use a locationSelector for \"every location in a city or region\", which also picks " +
			"up locations added later. The manifest is then passed to resources_plan and, once the person " +
			"agrees, resources_apply. Load the workload-create skill before using this. Writes nothing.",
	}, workloadRender(deps))
}

// ---------------------------------------------------------------- handlers

func workloadRender(deps DepsFor) mcp.ToolHandlerFor[WorkloadRenderInput, WorkloadRenderOutput] {
	return func(
		ctx context.Context, _ *mcp.CallToolRequest, in WorkloadRenderInput,
	) (*mcp.CallToolResult, WorkloadRenderOutput, error) {
		// Rendering reads nothing, but an unauthenticated caller must not be
		// able to use it as a probe, the same rule compute_reason_explain follows.
		if _, err := deps(ctx); err != nil {
			return nil, WorkloadRenderOutput{}, err
		}

		spec := toSpecInput(in)
		workload, err := workloadspec.Render(spec)
		if err != nil {
			return nil, WorkloadRenderOutput{}, err
		}
		manifest, err := workloadspec.MarshalYAML(workload)
		if err != nil {
			return nil, WorkloadRenderOutput{}, err
		}

		return nil, WorkloadRenderOutput{
			Manifest: string(manifest),
			Notes:    renderNotes(spec),
		}, nil
	}
}

// ---------------------------------------------------------------- rendering

// toSpecInput converts the tool's input to workloadspec's. A straight mapping,
// kept explicit so the tool schema can be worded for a model without that
// wording leaking into the renderer.
func toSpecInput(in WorkloadRenderInput) workloadspec.Input {
	out := workloadspec.Input{
		Name:         in.Name,
		Image:        in.Image,
		InstanceType: in.InstanceType,
		RuntimeClass: in.RuntimeClass,
		Network:      in.Network,
		PublicIPv4:   in.PublicIPv4,
		Labels:       in.Labels,
	}

	for _, p := range in.Placements {
		out.Placements = append(out.Placements, workloadspec.Placement{
			Name:             p.Name,
			Locations:        p.Locations,
			LocationSelector: toLabelSelector(p.LocationSelector),
			MinReplicas:      p.MinReplicas,
		})
	}
	for _, p := range in.Ports {
		out.Ports = append(out.Ports, workloadspec.Port{
			Name:     p.Name,
			Port:     p.Port,
			Protocol: corev1.Protocol(p.Protocol),
		})
	}
	for _, e := range in.Env {
		out.Env = append(out.Env, workloadspec.EnvVar{
			Name:            e.Name,
			Value:           e.Value,
			ConfigMapKeyRef: toKeyRef(e.ConfigMapKeyRef),
			SecretKeyRef:    toKeyRef(e.SecretKeyRef),
		})
	}
	for _, m := range in.ConfigMounts {
		out.ConfigMounts = append(out.ConfigMounts, workloadspec.Mount{
			Name:      m.Name,
			ConfigMap: m.ConfigMap,
			Secret:    m.Secret,
			MountPath: m.MountPath,
		})
	}
	if in.VM != nil {
		out.VM = &workloadspec.VMInput{
			SSHKeys:   in.VM.SSHKeys,
			BootImage: in.VM.BootImage,
		}
	}

	return out
}

// toLabelSelector converts the tool's selector to the API's. The operator is
// passed through verbatim: an unrecognized one is refused by the render's own
// validation with the field path, which is more useful than silently dropping
// the requirement here.
func toLabelSelector(sel *RenderLocationSelector) *metav1.LabelSelector {
	if sel == nil {
		return nil
	}
	out := &metav1.LabelSelector{MatchLabels: sel.MatchLabels}
	for _, req := range sel.MatchExpressions {
		out.MatchExpressions = append(out.MatchExpressions, metav1.LabelSelectorRequirement{
			Key:      req.Key,
			Operator: metav1.LabelSelectorOperator(req.Operator),
			Values:   req.Values,
		})
	}
	return out
}

func toKeyRef(ref *RenderKeyRef) *workloadspec.KeyRef {
	if ref == nil {
		return nil
	}
	return &workloadspec.KeyRef{Name: ref.Name, Key: ref.Key}
}

// renderNotes says what this manifest settled that a later render cannot
// correct, and which values were filled in for a caller who did not name them.
//
// It is written from the input as given, before defaults are applied, so
// "defaulted to" means the person did not choose it — which is the thing they
// need to be asked about while the workload can still be changed.
func renderNotes(in workloadspec.Input) []string {
	notes := []string{
		"The instance's single network interface is settled by this manifest and cannot be changed " +
			"once the workload exists: its name, the address families it carries, any extra addresses, " +
			"and what becomes of those addresses when an instance goes away. Getting one of them wrong " +
			"means creating a new workload, not editing this one.",
	}

	if in.PublicIPv4 {
		notes = append(notes, "A public IPv4 address was asked for, so the interface carries both IPv4 "+
			"and IPv6. Neither the address nor the families can be removed later.")
	} else {
		notes = append(notes, "The interface carries IPv6 only, which is the default. If this workload "+
			"has to answer on IPv4, say so before it is applied: IPv4 cannot be added afterwards.")
	}

	notes = append(notes, "Addresses are given back when an instance goes away. Keeping one — an "+
		"address published in DNS, or allowed through someone's firewall — means editing this manifest "+
		"before the first apply.")

	if in.InstanceType == "" {
		notes = append(notes, fmt.Sprintf(
			"No instance type was given, so every instance is %s. Per-container CPU and memory are not "+
				"accepted: the instance type is what decides the size.", workloadspec.DefaultInstanceType))
	}
	if in.Network == "" {
		notes = append(notes, fmt.Sprintf(
			"No network was named, so the interface attaches to %q. Check it exists with %s; if it does "+
				"not, plan a Network manifest of that name in the same %s call as this workload.",
			workloadspec.DefaultNetwork, baseToolResourcesList, baseToolResourcesPlan))
	}
	for _, p := range in.Placements {
		if p.Name == "" {
			notes = append(notes, fmt.Sprintf("A placement was not named, so it is called %q.",
				workloadspec.DefaultPlacementName))
		}
		if p.MinReplicas == 0 {
			notes = append(notes, fmt.Sprintf(
				"Placement %q did not say how many instances to run, so it runs %d. There is no "+
					"scaling to zero.", placementName(p), workloadspec.DefaultMinReplicas))
		}
		if p.LocationSelector != nil {
			notes = append(notes, fmt.Sprintf(
				"Placement %q selects its locations by topology rather than naming them, so it runs "+
					"wherever the selector matches — including locations added later, which will "+
					"start instances without this manifest changing. %s shows which locations match "+
					"today.", placementName(p), baseToolLocationsList))
		}
	}
	if in.VM != nil && in.VM.BootImage == "" {
		notes = append(notes, fmt.Sprintf(
			"No boot image was given, so the machine boots %s, currently the only one accepted.",
			workloadspec.DefaultBootImage))
	}

	return notes
}

func placementName(p workloadspec.Placement) string {
	if p.Name == "" {
		return workloadspec.DefaultPlacementName
	}
	return p.Name
}
