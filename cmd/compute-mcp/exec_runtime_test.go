package main

import (
	"context"
	"strings"
	"testing"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestLeaseNonceStoreConsumesOnceAndPrunesExpiredClaims(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := coordinationv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := &leaseNonceStore{client: c, namespace: "compute-system"}
	nonce := strings.Repeat("a", 32)
	if ok, err := store.Consume(ctx, nonce); err != nil || !ok {
		t.Fatalf("first claim = %t, %v", ok, err)
	}
	if ok, err := store.Consume(ctx, nonce); err != nil || ok {
		t.Fatalf("replay claim = %t, %v", ok, err)
	}
	if ok, err := store.Consume(ctx, "bad"); err == nil || ok {
		t.Fatal("invalid nonce accepted")
	}

	var lease coordinationv1.Lease
	key := client.ObjectKey{Namespace: "compute-system", Name: "assistant-exec-" + nonce}
	if err := c.Get(ctx, key, &lease); err != nil {
		t.Fatal(err)
	}
	lease.CreationTimestamp = metav1.NewTime(time.Now().Add(-25 * time.Hour))
	if err := c.Update(ctx, &lease); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, key, &lease); err == nil {
		t.Fatal("old nonce was not pruned")
	}
}
