package agent

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// Reader fetches the compute objects the tools reason over.
//
// The identity a read runs under is decided by whoever constructs the Reader,
// never buried in tool code: the server builds one per request from the
// caller's own credentials, so a tool call can never see more than the caller
// could see themselves.
type Reader interface {
	// ListWorkloads returns every Workload in the namespace.
	ListWorkloads(ctx context.Context, namespace string) ([]computev1alpha.Workload, error)
	// GetWorkload returns one Workload by name.
	GetWorkload(ctx context.Context, namespace, name string) (*computev1alpha.Workload, error)
	// ListDeployments returns the WorkloadDeployments belonging to a Workload.
	ListDeployments(ctx context.Context, namespace, workload string) ([]computev1alpha.WorkloadDeployment, error)
	// ListInstances returns the Instances belonging to a Workload.
	ListInstances(ctx context.Context, namespace, workload string) ([]computev1alpha.Instance, error)
}

// ClientReader implements Reader against a controller-runtime client.
type ClientReader struct {
	Client client.Client
}

var _ Reader = (*ClientReader)(nil)

// NewClientReader returns a Reader backed by c. Every read is performed with
// whatever credentials c carries.
func NewClientReader(c client.Client) *ClientReader {
	return &ClientReader{Client: c}
}

func (r *ClientReader) ListWorkloads(ctx context.Context, namespace string) ([]computev1alpha.Workload, error) {
	var list computev1alpha.WorkloadList
	if err := r.Client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing workloads in %s: %w", namespace, err)
	}
	return list.Items, nil
}

func (r *ClientReader) GetWorkload(ctx context.Context, namespace, name string) (*computev1alpha.Workload, error) {
	var w computev1alpha.Workload
	key := client.ObjectKey{Namespace: namespace, Name: name}
	if err := r.Client.Get(ctx, key, &w); err != nil {
		return nil, fmt.Errorf("getting workload %s/%s: %w", namespace, name, err)
	}
	return &w, nil
}

// ListDeployments filters on spec.workloadRef.Name rather than a label. The
// deployment carries the reference in its spec, and filtering client-side
// avoids depending on a field index the MCP server does not register.
func (r *ClientReader) ListDeployments(
	ctx context.Context, namespace, workload string,
) ([]computev1alpha.WorkloadDeployment, error) {
	var list computev1alpha.WorkloadDeploymentList
	if err := r.Client.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return nil, fmt.Errorf("listing workload deployments in %s: %w", namespace, err)
	}
	out := make([]computev1alpha.WorkloadDeployment, 0, len(list.Items))
	for _, d := range list.Items {
		if d.Spec.WorkloadRef.Name == workload {
			out = append(out, d)
		}
	}
	return out, nil
}

// ListInstances selects on WorkloadNameLabel, which the controllers stamp on
// every Instance with the Workload it ultimately belongs to.
func (r *ClientReader) ListInstances(
	ctx context.Context, namespace, workload string,
) ([]computev1alpha.Instance, error) {
	var list computev1alpha.InstanceList
	err := r.Client.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingLabels{computev1alpha.WorkloadNameLabel: workload},
	)
	if err != nil {
		return nil, fmt.Errorf("listing instances for workload %s/%s: %w", namespace, workload, err)
	}
	return list.Items, nil
}
