// SPDX-License-Identifier: AGPL-3.0-only

// This test runner starts the production publisher against a disposable project
// API without starting unrelated Compute controllers. It is not a product entrypoint.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/controller"
	"go.datum.net/compute/internal/features"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcsingle "sigs.k8s.io/multicluster-runtime/providers/single"
)

func main() {
	var kubeconfig, namespace, projectUID, sourceUID, subject, gates string
	var lease time.Duration
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	flags.StringVar(&kubeconfig, "kubeconfig", "", "Disposable project kubeconfig")
	flags.StringVar(&namespace, "namespace", "project-e2e", "Project namespace")
	flags.StringVar(&projectUID, "project-uid", "project-e2e-uid", "DNS project UID")
	flags.StringVar(&sourceUID, "source-uid", "internal-dns-e2e-cluster", "DNS trusted source UID")
	flags.StringVar(&subject, "subject", "system:serviceaccount:compute-system:dns-publisher", "Authenticated writer")
	flags.StringVar(&gates, "feature-gates", "", "Compute feature gates")
	flags.DurationVar(&lease, "lease", 60*time.Second, "Contribution lease")
	flags.Parse(os.Args[1:])
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	if err := features.MutableFeatureGate.Set(gates); err != nil {
		panic(err)
	}
	if !features.FeatureGate.Enabled(features.InternalDNSPublishing) {
		fmt.Println("InternalDNSPublishing is disabled; no DNS client or controller started")
		return
	}
	cfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		panic(err)
	}
	cfg.Timeout = 10 * time.Second
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, computev1alpha.AddToScheme, networkingv1alpha.AddToScheme} {
		if err := add(scheme); err != nil {
			panic(err)
		}
	}
	cl, err := cluster.New(cfg, func(o *cluster.Options) {
		o.Scheme = scheme
		o.Cache.DefaultNamespaces = map[string]cache.Config{namespace: {}}
	})
	if err != nil {
		panic(err)
	}
	mgr, err := mcmanager.New(cfg, mcsingle.New(multicluster.ClusterName("single"), cl), ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0",
		Cache: cache.Options{DefaultNamespaces: map[string]cache.Config{namespace: {}}},
	})
	if err != nil {
		panic(err)
	}
	publisher := &controller.InternalDNSPublisherReconciler{
		ProjectIdentities: map[string]controller.InternalDNSProjectIdentity{"single": {
			ProjectName: "single", ProjectUID: types.UID(projectUID), SourceClusterUID: sourceUID,
		}}, PrincipalSubject: subject, LeaseDuration: lease,
	}
	if err := publisher.SetupWithManager(mgr); err != nil {
		panic(err)
	}
	ctx, cancel := context.WithCancel(ctrl.SetupSignalHandler())
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- cl.Start(ctx) }()
	go func() { errs <- mgr.Start(ctx) }()
	if err := <-errs; err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
