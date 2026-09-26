// SPDX-License-Identifier: AGPL-3.0-only

package naming

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/types"
)

const testUID = types.UID("0f7c9a52-3b1e-4d8e-9a61-2f4c7b8e1d05")

func TestDeploymentName_Deterministic(t *testing.T) {
	a := DeploymentName("checkout-api", testUID, "production", "us-east-1")
	b := DeploymentName("checkout-api", testUID, "production", "us-east-1")
	assert.Equal(t, a, b)
	assert.True(t, strings.HasPrefix(a, "checkout-api-"))
	assert.Len(t, a, len("checkout-api-")+deploymentNameHashLength)
}

func TestDeploymentName_LocationCaseInsensitive(t *testing.T) {
	assert.Equal(t,
		DeploymentName("app", testUID, "default", "US-EAST-1"),
		DeploymentName("app", testUID, "default", "us-east-1"),
	)
}

func TestDeploymentName_LengthBound(t *testing.T) {
	long63 := strings.Repeat("a", 63)
	name := DeploymentName(long63, testUID, long63, long63)
	assert.Len(t, name, MaxDeploymentNameLength)
	assert.LessOrEqual(t, MaxDeploymentNameLength, 41)
	assert.Empty(t, validation.NameIsDNSLabel(name, false))
}

func TestDeploymentName_DistinctInputs(t *testing.T) {
	assert.NotEqual(t,
		DeploymentName("app-a", testUID, "b", "loc"),
		DeploymentName("app", testUID, "a-b", "loc"),
		"joining with a dash must not make different placements collide",
	)
	assert.NotEqual(t,
		DeploymentName("app", testUID, "default", "loc-a"),
		DeploymentName("app", testUID, "default", "loc-b"),
	)
	assert.NotEqual(t,
		DeploymentName("app", testUID, "default", "loc"),
		DeploymentName("app", types.UID("another-uid"), "default", "loc"),
		"a recreated workload must get fresh names",
	)
}

func TestDeploymentName_TrimsTrailingDash(t *testing.T) {
	workloadName := strings.Repeat("a", 29) + "-bcd"
	name := DeploymentName(workloadName, testUID, "default", "loc")
	assert.True(t, strings.HasPrefix(name, strings.Repeat("a", 29)+"-"))
	assert.NotContains(t, name, "--")
	assert.Empty(t, validation.NameIsDNSLabel(name, false))
}
