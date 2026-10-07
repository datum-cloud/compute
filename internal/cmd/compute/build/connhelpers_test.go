package build

import (
	"testing"

	"github.com/moby/buildkit/client/connhelper"
)

func TestBuildKitConnectionHelpersRegistered(t *testing.T) {
	for _, addr := range []string{
		"docker-container://buildx_buildkit_builder0",
		"podman-container://buildkitd",
		"nerdctl-container://buildkitd",
		"kube-pod://buildkitd",
		"ssh://user@example.com",
	} {
		helper, err := connhelper.GetConnectionHelper(addr)
		if err != nil {
			t.Fatalf("%s: %v", addr, err)
		}
		if helper == nil {
			t.Fatalf("%s: no connection helper registered", addr)
		}
	}
}
