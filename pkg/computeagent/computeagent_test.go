// SPDX-License-Identifier: AGPL-3.0-only

package computeagent_test

import (
	"context"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.datum.net/compute/pkg/computeagent"
)

// A server outside this module mounts exactly the tools the assistant gets.
// If a tool is added to internal/agent and not to these constants, or the
// other way round, this fails rather than leaving an importer a name that
// registers nothing.
func TestRegisterToolsExposesEveryNamedTool(t *testing.T) {
	ctx := context.Background()
	s := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	computeagent.RegisterTools(s, func(context.Context) (computeagent.ToolDeps, error) {
		return computeagent.ToolDeps{}, nil
	})

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	_, err := s.Connect(ctx, serverTransport, nil)
	require.NoError(t, err)
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	defer func() { _ = session.Close() }()

	res, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	got := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		// None of these writes; a client may run them without asking first.
		assert.True(t, tool.Annotations != nil && tool.Annotations.ReadOnlyHint, "%s is not annotated read-only", tool.Name)
	}
	sort.Strings(got)

	want := []string{
		computeagent.ToolInstanceTypesList,
		computeagent.ToolInstancesList,
		computeagent.ToolReasonExplain,
		computeagent.ToolWorkloadDiagnose,
		computeagent.ToolWorkloadRender,
		computeagent.ToolWorkloadsGet,
		computeagent.ToolWorkloadsList,
		computeagent.ToolExecSuggest,
	}
	sort.Strings(want)
	assert.Equal(t, want, got)
}

func TestNewSchemeDecodesWorkloads(t *testing.T) {
	scheme, err := computeagent.NewScheme()
	require.NoError(t, err)
	for _, kind := range []string{"Workload", "WorkloadDeployment", "Instance"} {
		found := false
		for gvk := range scheme.AllKnownTypes() {
			if gvk.Group == "compute.datumapis.com" && gvk.Kind == kind {
				found = true
				break
			}
		}
		assert.True(t, found, "scheme is missing %s", kind)
	}
}
