package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"go.datum.net/compute/internal/assistantorigin"
)

const execNonceNamespaceEnv = "POD_NAMESPACE"
const execNonceLabel = "compute.datumapis.com/assistant-exec-nonce"

// execRuntime is absent unless the deployment has a shared key and a local
// Kubernetes API for its atomic nonce ledger. A partial configuration fails
// startup; it must never fall back to a per-process replay cache.
type execRuntime struct {
	key    []byte
	nonces *leaseNonceStore
}

func newExecRuntime() (*execRuntime, error) {
	key, err := assistantorigin.KeyFromEnv()
	if err != nil {
		return nil, err
	}
	if len(key) == 0 {
		return nil, nil
	}
	namespace := os.Getenv(execNonceNamespaceEnv)
	if errs := validation.IsDNS1123Label(namespace); len(errs) > 0 {
		return nil, fmt.Errorf("%s must name the MCP pod namespace: %v", execNonceNamespaceEnv, errs)
	}
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("exec nonce ledger needs in-cluster credentials: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("exec nonce ledger client: %w", err)
	}
	return &execRuntime{key: key, nonces: &leaseNonceStore{client: c, namespace: namespace}}, nil
}

type leaseNonceStore struct {
	client    client.Client
	namespace string
}

// Consume uses a Kubernetes create as the shared compare-and-set across all
// compute-mcp replicas. The lease remains after completion because the shell
// controller deletes completed sessions, so retaining it prevents replay.
func (s *leaseNonceStore) Consume(ctx context.Context, nonce string) (bool, error) {
	if len(nonce) != 32 {
		return false, errors.New("invalid exec nonce")
	}
	for _, ch := range nonce {
		if ch < '0' || (ch > '9' && ch < 'a') || ch > 'f' {
			return false, errors.New("invalid exec nonce")
		}
	}
	lease := &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Name: "assistant-exec-" + nonce, Namespace: s.namespace,
		Labels: map[string]string{execNonceLabel: "true"},
	}}
	if err := s.client.Create(ctx, lease); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// Prune removes consumed nonces only after every corresponding 5-minute plan
// has long expired. A 24-hour retention also tolerates clock skew and delayed
// tool retries while keeping the ledger bounded over time.
func (s *leaseNonceStore) Prune(ctx context.Context) error {
	var leases coordinationv1.LeaseList
	if err := s.client.List(ctx, &leases, client.InNamespace(s.namespace),
		client.MatchingLabels{execNonceLabel: "true"}); err != nil {
		return err
	}
	for i := range leases.Items {
		lease := &leases.Items[i]
		if time.Since(lease.CreationTimestamp.Time) < 24*time.Hour {
			continue
		}
		if err := s.client.Delete(ctx, lease); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return nil
}
