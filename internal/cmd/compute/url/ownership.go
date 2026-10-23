// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// ErrNotCreatedByDatumctl is returned when publishing would have to take over
// an object of the workload's name that datumctl did not create.
var ErrNotCreatedByDatumctl = errors.New("not created by datumctl")

// The words a user sees for the two objects behind a URL.
const (
	kindLoadBalancer = "load balancer"
	kindBackends     = "load balancer backends"
)

// Foreign is an object of a workload's name that datumctl did not create — set
// up in the portal, by hand, or by other tooling. It belongs to whoever did,
// so datumctl neither updates nor deletes it.
type Foreign struct {
	Kind string
	Name string
}

func (f Foreign) String() string { return fmt.Sprintf("%s %q", f.Kind, f.Name) }

// ForeignObjects returns the objects named after the workload that datumctl
// did not create. Publishing would have to take them over, and unpublishing
// leaves them in place.
func ForeignObjects(ctx context.Context, c client.Client, workloadName string) ([]Foreign, error) {
	key := client.ObjectKey{Namespace: util.ResourceNamespace, Name: ResourceName(workloadName)}

	var foreign []Foreign
	for _, o := range []struct {
		kind string
		obj  client.Object
	}{
		{kindLoadBalancer, &networkingv1alpha.HTTPProxy{}},
		{kindBackends, &networkingv1alpha.NetworkService{}},
	} {
		if err := c.Get(ctx, key, o.obj); err != nil {
			if notPublished(err) {
				continue
			}
			return nil, fmt.Errorf("reading %s %q: %w", o.kind, key.Name, err)
		}
		if !isWorkloadsOwn(o.obj, workloadName) {
			foreign = append(foreign, Foreign{Kind: o.kind, Name: key.Name})
		}
	}
	return foreign, nil
}

// CheckOwnership returns an error wrapping ErrNotCreatedByDatumctl if
// publishing the workload would have to take over an object datumctl did not
// create. A deploy calls it while planning, so it stops before anything is
// applied; Declare calls it again, so neither object is written when the other
// is refused.
func CheckOwnership(ctx context.Context, c client.Client, workloadName string) error {
	foreign, err := ForeignObjects(ctx, c, workloadName)
	if err != nil {
		return err
	}
	if len(foreign) > 0 {
		return &notCreatedError{foreign: foreign}
	}
	return nil
}

// isWorkloadsOwn reports whether datumctl created obj for this workload. Every
// object it writes carries the workload-name label, so an object without it
// was set up some other way.
//
// The workload UID is deliberately not compared: a workload destroyed and
// deployed again under the same name gets a new UID, and its URL is still its
// own.
func isWorkloadsOwn(obj client.Object, workloadName string) bool {
	return obj.GetLabels()[computev1alpha.WorkloadNameLabel] == workloadName
}

// createdByDatumctl reports whether an existing object is one datumctl wrote
// for the same workload as desired.
func createdByDatumctl(existing, desired client.Object) bool {
	name := desired.GetLabels()[computev1alpha.WorkloadNameLabel]
	return name != "" && isWorkloadsOwn(existing, name)
}

// notCreatedError explains why publishing stopped rather than taking over
// objects someone else set up. Updating them would have rewritten their
// routing and hostnames on the strength of a name clash.
type notCreatedError struct {
	foreign []Foreign
}

func (e *notCreatedError) Error() string {
	names := make([]string, 0, len(e.foreign))
	for _, f := range e.foreign {
		names = append(names, f.String())
	}
	verb := "exists"
	if len(names) > 1 {
		verb = "exist"
	}
	return fmt.Sprintf("%s already %s in this project and datumctl did not create it, "+
		"so a Datum-managed URL cannot be published under the workload's name. "+
		"To keep serving the workload through it, deploy without --http-port or --http2; "+
		"to publish a Datum-managed URL instead, remove it first",
		strings.Join(names, " and "), verb)
}

func (e *notCreatedError) Is(target error) bool { return target == ErrNotCreatedByDatumctl }
