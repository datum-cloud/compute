// SPDX-License-Identifier: AGPL-3.0-only

package agent

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.datum.net/compute/pkg/instancetype"
)

// ToolInstanceTypesList lists the instance types a Workload may ask for.
//
// A model with no catalog invents a plausible size, and the API rejects it the
// moment it is submitted. The catalog is compiled into compute rather than
// published as a resource, so the assistant's generic resource tools cannot
// read it and compute has to.
const ToolInstanceTypesList = "compute_instance_types_list"

// InstanceTypeView is one instance type a Workload may ask for.
type InstanceTypeView struct {
	Name string `json:"name"`
	// VCPU is how many virtual CPUs the type provides. Fractional, because the
	// size is stored in thousandths and a future type need not be a whole one.
	VCPU float64 `json:"vcpu"`
	// MemoryMiB is the RAM the type provides, in mebibytes.
	MemoryMiB int64 `json:"memoryMiB"`
	// Default marks the type to use when the customer expressed no preference.
	Default bool `json:"default"`
}

// InstanceTypesListInput takes no arguments.
type InstanceTypesListInput struct{}

// InstanceTypesListOutput is the catalog of instance types.
type InstanceTypesListOutput struct {
	InstanceTypes []InstanceTypeView `json:"instanceTypes"`
}

// registerInstanceTypesTool adds compute_instance_types_list to s.
func registerInstanceTypesTool(s *mcp.Server, deps DepsFor) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  ToolInstanceTypesList,
		Title: "List instance types",
		Description: "List the instance types a Workload may ask for, with the vCPU and memory each one " +
			"provides and which is the default. Only these names are accepted — a Workload naming any " +
			"other is rejected the moment it is submitted, so never invent a size. Read-only.",
	}, instanceTypesList(deps))
}

// instanceTypesList reads only the catalog, but still resolves deps for the
// same reason reasonExplain does: an unauthenticated caller must not be able to
// use it to probe the server.
func instanceTypesList(deps DepsFor) mcp.ToolHandlerFor[InstanceTypesListInput, InstanceTypesListOutput] {
	return func(
		ctx context.Context, _ *mcp.CallToolRequest, _ InstanceTypesListInput,
	) (*mcp.CallToolResult, InstanceTypesListOutput, error) {
		if _, err := deps(ctx); err != nil {
			return nil, InstanceTypesListOutput{}, err
		}
		return nil, InstanceTypesListOutput{InstanceTypes: InstanceTypes()}, nil
	}
}

// InstanceTypes returns the instance types a Workload may ask for, in offer
// order. The first is the default: the order the platform's catalog lists them
// in is the order to prefer them.
//
// Names and sizes both come from pkg/instancetype, the same table the instance
// controller claims quota against. Restating either here would let the tool
// offer a size the API bills differently.
func InstanceTypes() []InstanceTypeView {
	names := instancetype.Names()
	out := make([]InstanceTypeView, 0, len(names))
	for i, name := range names {
		size, _ := instancetype.Lookup(name)
		out = append(out, InstanceTypeView{
			Name:      name,
			VCPU:      float64(size.CPUMillicores) / 1000,
			MemoryMiB: size.MemoryMiB,
			Default:   i == 0,
		})
	}
	return out
}
