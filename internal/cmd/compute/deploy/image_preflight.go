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
	return checkImageRuntimeIn(ctx, c, image, class, platforms, flagClassHint)
}

// classHint tells the user how to select a runtime class where they deploy.
type classHint func(class string) string

func flagClassHint(class string) string { return "--runtime-class=" + class }

func manifestClassHint(class string) string {
	return "spec.template.spec.runtime.class: " + class + " in the manifest"
}

func checkImageRuntimeIn(ctx context.Context, c client.Client, image, class string, platforms imagePlatformsFunc, hint classHint) error {
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
			msg += "or run it as a container with " + hint(alt)
		}
		return fmt.Errorf("%s", strings.TrimSpace(msg))
	case !unikernel && !hasContainer:
		msg := fmt.Sprintf("image %q is a Compute image (%s), which the %q runtime class cannot run as a container.",
			image, strings.Join(got, ", "), selected.Name)
		if alt := classWithBoundary(classes.Items, func(b string) bool { return b == isolationUnikernel }); alt != "" {
			msg += "\n\nDeploy it with " + hint(alt)
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

// checkManifestImages runs checkImageRuntime on every container a manifest
// declares, in the class the workload will run in: the manifest's, else the
// existing workload's, else the catalog default.
func checkManifestImages(ctx context.Context, c client.Client, manifest, existing *computev1alpha.Workload, platforms imagePlatformsFunc) error {
	sandbox := manifest.Spec.Template.Spec.Runtime.Sandbox
	if sandbox == nil {
		return nil
	}
	class := manifest.Spec.Template.Spec.Runtime.Class
	if class == "" && existing != nil {
		class = existing.Spec.Template.Spec.Runtime.Class
	}
	for _, container := range sandbox.Containers {
		if err := checkImageRuntimeIn(ctx, c, container.Image, class, platforms, manifestClassHint); err != nil {
			return err
		}
	}
	return nil
}

// planRuntimeClass names the class a deploy writes for its plan. A new
// workload with no class gets the catalog default, so the user sees the tier
// before applying. An existing workload with no class predates runtime
// classes and is left unnamed.
func planRuntimeClass(ctx context.Context, c client.Client, class string, creating bool) string {
	if class != "" || !creating {
		return class
	}
	var classes computev1alpha.RuntimeClassList
	if err := c.List(ctx, &classes); err == nil {
		if selected := selectRuntimeClass(classes.Items, ""); selected != nil {
			return selected.Name + " (platform default)"
		}
	}
	return "platform default"
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
