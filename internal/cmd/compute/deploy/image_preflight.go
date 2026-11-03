// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/build"
)

// isolationUnikernel is the RuntimeClass isolation boundary of unikernels,
// the one tier that boots Compute images and nothing else.
const isolationUnikernel = "unikernel"

type imagePlatformsFunc func(ctx context.Context, image string) ([]string, error)

// checkImageRuntime refuses an image the workload's runtime class cannot
// boot, before anything is written. The unikernel tier needs a Compute
// variant in the image index, and every other tier needs a container one.
//
// The runtime, not this check, is the authority on what pulls. A catalog
// the client cannot list, a class it does not list, or an image the
// registry will not show from this machine all let the deploy proceed.
func checkImageRuntime(ctx context.Context, c client.Client, image, class string, platforms imagePlatformsFunc) error {
	var classes computev1alpha.RuntimeClassList
	if err := c.List(ctx, &classes); err != nil {
		return nil
	}
	selected := selectRuntimeClass(classes.Items, class)
	if selected == nil {
		return nil
	}
	got, err := platforms(ctx, image)
	if err != nil || len(got) == 0 {
		return nil
	}

	hasCompute := slices.ContainsFunc(got, build.IsComputePlatform)
	hasContainer := slices.ContainsFunc(got, func(p string) bool { return !build.IsComputePlatform(p) })
	unikernel := selected.Spec.Isolation.Boundary == isolationUnikernel
	switch {
	case unikernel && !hasCompute:
		msg := fmt.Sprintf("image %q is a container image (%s), not a Compute image, so the %q runtime class cannot boot it.\n\n"+
			"Build it for Compute and deploy the result:\n"+
			"  datumctl compute build --push --output <ref> .\n"+
			"  datumctl compute deploy ... --image <ref>\n",
			image, strings.Join(got, ", "), selected.Name)
		if alt := classWithBoundary(classes.Items, func(b string) bool { return b != isolationUnikernel }); alt != "" {
			msg += fmt.Sprintf("or run it as a container with --runtime-class=%s", alt)
		}
		return fmt.Errorf("%s", strings.TrimSpace(msg))
	case !unikernel && !hasContainer:
		msg := fmt.Sprintf("image %q is a Compute image (%s), which the %q runtime class cannot run as a container.",
			image, strings.Join(got, ", "), selected.Name)
		if alt := classWithBoundary(classes.Items, func(b string) bool { return b == isolationUnikernel }); alt != "" {
			msg += fmt.Sprintf("\n\nDeploy it with --runtime-class=%s", alt)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// selectRuntimeClass returns the class named, or the catalog default when
// none is, the way admission fills in an empty class.
func selectRuntimeClass(classes []computev1alpha.RuntimeClass, name string) *computev1alpha.RuntimeClass {
	for i := range classes {
		c := &classes[i]
		if c.Name == name || (name == "" && c.Spec.Default) {
			return c
		}
	}
	return nil
}

func classWithBoundary(classes []computev1alpha.RuntimeClass, match func(string) bool) string {
	for _, c := range classes {
		if match(c.Spec.Isolation.Boundary) {
			return c.Name
		}
	}
	return ""
}
