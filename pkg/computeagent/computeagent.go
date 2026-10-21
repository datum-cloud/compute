// SPDX-License-Identifier: AGPL-3.0-only

// Package computeagent is the importable surface of compute's agent tools, for
// an MCP server outside this module that wants to offer them — datum-mcp, which
// runs on a customer's own machine and so cannot reach the in-cluster
// compute-mcp.
//
// Everything here is a thin re-export of internal/agent. The diagnosis walk,
// command suggestions, the reason catalog and the rendering stay internal and singular, so a second
// server mounting these tools describes a workload exactly the way the
// assistant and the compute plugin do. Do not add logic here; add it to
// internal/agent and re-export it.
//
// The knowledge and skills that go with these tools are already importable from
// go.datum.net/compute/docs/agent.
package computeagent

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/agent"
)

// The tool names RegisterTools adds. Each carries the compute_ prefix so they
// can sit beside another server's tools without colliding.
const (
	ToolWorkloadsList     = agent.ToolWorkloadsList
	ToolWorkloadsGet      = agent.ToolWorkloadsGet
	ToolInstancesList     = agent.ToolInstancesList
	ToolWorkloadDiagnose  = agent.ToolWorkloadDiagnose
	ToolReasonExplain     = agent.ToolReasonExplain
	ToolInstanceTypesList = agent.ToolInstanceTypesList
	ToolWorkloadRender    = agent.ToolWorkloadRender
	ToolExecSuggest       = agent.ToolExecSuggest
)

// Namespace is the namespace every compute object lives in within a project
// control plane.
const Namespace = "default"

type (
	// Reader fetches the objects a workload is assembled from. It has no write
	// method, by design; see internal/agent.Reader.
	Reader = agent.Reader
	// ToolDeps is what one request's tool calls read through.
	ToolDeps = agent.ToolDeps
	// DepsFor resolves ToolDeps per call, so the caller decides how identity
	// and project are established.
	DepsFor = agent.DepsFor
)

// RegisterTools adds every compute tool to s. None of them writes.
func RegisterTools(s *mcp.Server, deps DepsFor) {
	agent.RegisterTools(s, deps)
}

// NewClientReader returns a Reader backed by c, which must be built with a
// scheme from NewScheme. Every read runs with whatever credentials c carries.
func NewClientReader(c client.Client) Reader {
	return agent.NewClientReader(c)
}

// NewScheme returns a scheme registering every type a Reader decodes.
func NewScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering core scheme: %w", err)
	}
	if err := computev1alpha.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering compute scheme: %w", err)
	}
	return scheme, nil
}
