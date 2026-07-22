// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"github.com/KimMachineGun/automemlimit/memlimit"
	"golang.org/x/sync/errgroup"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcsingle "sigs.k8s.io/multicluster-runtime/providers/single"

	karmadaclusterv1alpha1 "github.com/karmada-io/api/cluster/v1alpha1"
	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/config"
	"go.datum.net/compute/internal/controller"
	"go.datum.net/compute/internal/features"
	quotametrics "go.datum.net/compute/internal/quota"
	computewebhook "go.datum.net/compute/internal/webhook"
	computev1alphawebhooks "go.datum.net/compute/internal/webhook/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	infrastructurev1alpha1 "go.miloapis.com/milo/pkg/apis/infrastructure/v1alpha1"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	resourcemanagerv1alpha1 "go.miloapis.com/milo/pkg/apis/resourcemanager/v1alpha1"
	multiclusterproviders "go.miloapis.com/milo/pkg/multicluster-runtime"
	milomulticluster "go.miloapis.com/milo/pkg/multicluster-runtime/milo"
	// +kubebuilder:scaffold:imports
)

// singleClusterName is the fixed cluster name that mcsingle.New registers.
// All single-mode wiring that references this cluster must use this constant.
const singleClusterName = "single"

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
	codecs   = serializer.NewCodecFactory(scheme, serializer.EnableStrict)

	// Build metadata, set via -ldflags at build time. See Dockerfile.
	version      = "dev"
	gitCommit    = "unknown"
	gitTreeState = "unknown"
	buildDate    = "unknown"

	// federationRestConfig holds the REST config for the Karmada federation control
	// plane. It is populated from --federation-kubeconfig when set, and is nil
	// when the flag is omitted.
	federationRestConfig *rest.Config
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(config.AddToScheme(scheme))
	utilruntime.Must(config.RegisterDefaults(scheme))
	utilruntime.Must(computev1alpha.AddToScheme(scheme))
	utilruntime.Must(networkingv1alpha.AddToScheme(scheme))
	utilruntime.Must(quotav1alpha1.AddToScheme(scheme))
	utilruntime.Must(karmadapolicyv1alpha1.Install(scheme))
	utilruntime.Must(karmadaclusterv1alpha1.Install(scheme))

	// +kubebuilder:scaffold:scheme
}

//nolint:gocyclo // main wires all controller paths; complexity is inherent to startup sequencing
func main() {

	var enableLeaderElection bool
	var leaderElectionNamespace string
	var probeAddr string
	var serverConfigFile string
	var federationKubeconfig string
	var federationContext string
	var enableManagementControllers bool
	var enableCellControllers bool

	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&leaderElectionNamespace, "leader-elect-namespace", "", "The namespace to use for leader election.")
	flag.StringVar(&federationKubeconfig, "federation-kubeconfig", "",
		"Path to the kubeconfig file for the Karmada federation control plane. "+
			"Required when --enable-management-controllers is set. "+
			"When omitted, federation features are disabled.")
	flag.StringVar(&federationContext, "federation-context", "",
		"Context to use from the federation kubeconfig. When omitted, the current context is used.")
	flag.BoolVar(&enableManagementControllers, "enable-management-controllers", false,
		"Enable management-plane controllers (WorkloadDeploymentFederator, InstanceProjector).")
	flag.BoolVar(&enableCellControllers, "enable-cell-controllers", false,
		"Enable cell controllers (WorkloadDeploymentReconciler, InstanceReconciler).")

	var featureGatesFlag string
	flag.StringVar(&featureGatesFlag, "feature-gates", "",
		"A set of key=value pairs that describe feature gates for the compute operator. "+
			"Example: --feature-gates=NetworkingIntegration=true. "+
			"Available features: NetworkingIntegration (default=false).")

	opts := zap.Options{
		Development: true,
	}

	flag.StringVar(&serverConfigFile, "server-config", "", "path to the server config file")

	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	if featureGatesFlag != "" {
		if err := features.MutableFeatureGate.Set(featureGatesFlag); err != nil {
			setupLog.Error(err, "unable to parse feature gates", "feature-gates", featureGatesFlag)
			os.Exit(1)
		}
	}
	setupLog.Info("feature gates", "NetworkingIntegration", features.FeatureGate.Enabled(features.NetworkingIntegration))

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Set GOMEMLIMIT from the container's cgroup memory limit so the Go GC
	// backpressures before the kernel OOMKills the process. Without this the
	// runtime is blind to the cgroup limit and lets the heap overshoot during
	// allocation-heavy startup (concurrent informer/cache sync across project
	// control planes), which is what OOMKilled compute-manager on rollout (#161).
	//
	// This is a no-op when GOMEMLIMIT is already set in the environment or
	// AUTOMEMLIMIT=off, so an explicit manifest override always wins. Off-cgroup
	// (e.g. local dev on macOS) the provider errors; we log and continue.
	if limit, err := memlimit.SetGoMemLimitWithOpts(memlimit.WithRatio(0.9)); err != nil {
		setupLog.Info("GOMEMLIMIT not set from cgroup; using Go default", "reason", err.Error())
	} else if limit > 0 {
		setupLog.Info("GOMEMLIMIT set from cgroup", "bytes", limit)
	}

	if federationKubeconfig != "" {
		loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: federationKubeconfig},
			&clientcmd.ConfigOverrides{CurrentContext: federationContext},
		)
		var err error
		federationRestConfig, err = loader.ClientConfig()
		if err != nil {
			setupLog.Error(err, "unable to load federation kubeconfig", "path", federationKubeconfig)
			os.Exit(1)
		}
		setupLog.Info("federation kubeconfig loaded", "path", federationKubeconfig)
	}

	// Fail loud: management controllers require a federation kubeconfig. Silently
	// skipping them when --enable-management-controllers is set would leave
	// federation and instance projection broken with no visible signal.
	if enableManagementControllers && federationRestConfig == nil {
		setupLog.Error(nil,
			"management controllers enabled but no federation kubeconfig configured",
			"hint", "set --federation-kubeconfig")
		os.Exit(1)
	}

	setupLog.Info("starting compute",
		"version", version,
		"gitCommit", gitCommit,
		"gitTreeState", gitTreeState,
		"buildDate", buildDate,
	)

	serverConfig, err := loadServerConfig(serverConfigFile)
	if err != nil {
		setupLog.Error(err, "unable to load server config")
		os.Exit(1)
	}

	setupLog.Info("server config", "config", serverConfig)

	quotaRestConfig, err := serverConfig.Discovery.QuotaRestConfig()
	if err != nil {
		setupLog.Error(err, "unable to load quota REST config")
		os.Exit(1)
	}
	if quotaRestConfig != nil {
		setupLog.Info("quota REST config loaded", "path", serverConfig.Discovery.QuotaKubeconfigPath)
		quotametrics.EnforcementEnabled.Set(1)
	} else {
		setupLog.Error(nil, "quota enforcement is DISABLED — workloads will schedule without quota accounting; "+
			"set quotaKubeconfigPath in server config to enable enforcement")
		quotametrics.EnforcementEnabled.Set(0)
	}

	cfg := ctrl.GetConfigOrDie()

	deploymentCluster, err := cluster.New(cfg, func(o *cluster.Options) {
		o.Scheme = scheme
		// Strip managedFields from every cached object. They are pure server-side
		// apply bookkeeping the controllers never read, and on large/unstructured
		// objects they dominate the per-object footprint. Dropping them shrinks the
		// startup cache-sync spike that OOMKilled compute-manager (#161).
		o.Cache.DefaultTransform = cache.TransformStripManagedFields()
	})
	if err != nil {
		setupLog.Error(err, "failed creating local cluster")
		os.Exit(1)
	}

	runnables, provider, edgeClusterName, discoveryCache, err := initializeClusterDiscovery(
		serverConfig, deploymentCluster, scheme,
	)
	if err != nil {
		setupLog.Error(err, "unable to initialize cluster discovery")
		os.Exit(1)
	}

	setupLog.Info("cluster discovery mode", "mode", serverConfig.Discovery.Mode)

	ctx := ctrl.SetupSignalHandler()

	deploymentClusterClient := deploymentCluster.GetClient()

	metricsServerOptions := serverConfig.MetricsServer.Options(ctx, deploymentClusterClient)

	var webhookServer webhook.Server
	if serverConfig.WebhookServer != nil {
		webhookServer = webhook.NewServer(
			serverConfig.WebhookServer.Options(ctx, deploymentClusterClient),
		)

		if serverConfig.Discovery.Mode != multiclusterproviders.ProviderSingle {
			webhookServer = computewebhook.NewClusterAwareWebhookServer(webhookServer)
		}
	} else {
		setupLog.Info("webhookServer not configured; admission webhook server disabled")
	}

	// mcmanager.Start auto-adds provider.Start as a plain manager.RunnableFunc
	// when the provider implements multicluster.ProviderRunnable. A Runnable
	// added that way implements neither NeedLeaderElection, so
	// controller-runtime's runnable group routes it into the leader-election
	// group for backwards compatibility (see runnable_group.go). That silently
	// leader-gates cluster engagement: on every pod but the elected leader,
	// provider.Start never runs, p.mcAware stays nil forever, and Reconcile
	// requeues indefinitely without ever engaging a single cluster -- so
	// GetCluster fails permanently on non-leader replicas, not just during a
	// startup race (datum-cloud/compute#117).
	//
	// Cluster engagement is local, per-pod cache/watch bookkeeping, not
	// cluster-mutating reconciliation -- the same category of thing as the
	// manager's own informer cache, which controller-runtime deliberately
	// never leader-gates. It must run on every replica, since the webhook
	// Service can route a request to any pod. milomulticluster.WithoutAutoStart
	// hides the Start method so mcmanager does not auto-wire it, and the block
	// below re-adds it directly, forced into the always-running group.
	mcProvider := provider
	if p, ok := provider.(*milomulticluster.Provider); ok {
		mcProvider = milomulticluster.WithoutAutoStart(p)
	}
	mgr, err := mcmanager.New(cfg, mcProvider, ctrl.Options{
		Scheme:                  scheme,
		Cache:                   cache.Options{DefaultTransform: cache.TransformStripManagedFields()},
		Metrics:                 metricsServerOptions,
		WebhookServer:           webhookServer,
		HealthProbeBindAddress:  probeAddr,
		LeaderElection:          enableLeaderElection,
		LeaderElectionID:        "e98b06c6.datumapis.com",
		LeaderElectionNamespace: leaderElectionNamespace,
		// LeaderElectionReleaseOnCancel defines if the leader should step down voluntarily
		// when the Manager ends. This requires the binary to immediately end when the
		// Manager is stopped, otherwise, this setting is unsafe. Setting this significantly
		// speeds up voluntary leader transitions as the new leader don't have to wait
		// LeaseDuration time first.
		//
		// In the default scaffold provided, the program ends immediately after
		// the manager stops, so would be fine to enable this option. However,
		// if you are doing or is intended to do any operation such as perform cleanups
		// after the manager stops then its usage might be unsafe.
		// LeaderElectionReleaseOnCancel: true,
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		os.Exit(1)
	}

	// Re-add cluster engagement as a runnable that always runs, regardless of
	// leader-election state (see the comment above the mcmanager.New call).
	// engageTracker records which clusters actually complete Engage so
	// readiness can require real engagement rather than only knowing a
	// project exists.
	var engaged *engageTracker
	if providerRunnable, ok := provider.(multicluster.ProviderRunnable); ok {
		engaged = newEngageTracker(mgr)
		if err := mgr.GetLocalManager().Add(nonLeaderElectionRunnable{
			Runnable: manager.RunnableFunc(func(ctx context.Context) error {
				return providerRunnable.Start(ctx, engaged)
			}),
		}); err != nil {
			setupLog.Error(err, "unable to add cluster provider runnable")
			os.Exit(1)
		}
	}

	if enableManagementControllers {
		if err = (&controller.WorkloadReconciler{}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Workload")
			os.Exit(1)
		}
	}

	// Build a single federation client shared across all controllers that need to
	// read or write to the Karmada federation control plane. This is the hub that
	// the management controllers federate through and that edge cells write back to.
	var federationClient client.Client
	if federationRestConfig != nil {
		federationClient, err = client.New(federationRestConfig, client.Options{Scheme: scheme})
		if err != nil {
			setupLog.Error(err, "unable to create federation client")
			os.Exit(1)
		}
	}

	if enableCellControllers {
		wdOpts := controller.WorkloadDeploymentReconcilerOptions{
			EnableReferencedDataGate: serverConfig.FeatureFlags.EnableReferencedDataGate,
		}
		if err = (&controller.WorkloadDeploymentReconciler{
			NetworkingEnabled: features.FeatureGate.Enabled(features.NetworkingIntegration),
		}).SetupWithManager(mgr, wdOpts); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "WorkloadDeployment")
			os.Exit(1)
		}
	}

	if enableCellControllers {
		clusterNameForProject := func(_ string) multicluster.ClusterName {
			return multicluster.ClusterName(singleClusterName)
		}

		// Quota claim writes are REST calls to the owning project's quota API and
		// need no local CRD, so they run in every mode. The ResourceClaim watch
		// needs the quota CRD, which lives only on the Milo project control planes,
		// so it is registered only in management-plane (milo) mode; engaging it in
		// single/cluster mode would crash the manager against a cell that has no
		// quota CRD (#171). There, grants are observed via the pending-quota
		// requeue instead of a live watch.
		watchProviderClaims := computeWatchProviderClaims(serverConfig.Discovery.Mode)
		if quotaRestConfig != nil && !watchProviderClaims {
			setupLog.Info("quota claim writes enabled without a ResourceClaim watch; "+
				"grants are observed via the pending-quota requeue", "mode", serverConfig.Discovery.Mode)
		}

		instanceReconciler := &controller.InstanceReconciler{
			FederationClient:  federationClient,
			NetworkingEnabled: features.FeatureGate.Enabled(features.NetworkingIntegration),
		}
		err = instanceReconciler.SetupWithManager(
			mgr,
			quotaRestConfig,
			controller.NewSingleModeProjectID(mgr),
			controller.NewSingleModeProjectNamespace(mgr),
			edgeClusterName,
			clusterNameForProject,
			watchProviderClaims,
		)
		if err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Instance")
			os.Exit(1)
		}
	}

	// The fail-loud guard above ensures federationRestConfig is non-nil when
	// management controllers are enabled; the nil check here is defensive.
	if enableManagementControllers && federationRestConfig != nil {
		extra, err := setupManagementControllers(mgr, federationClient)
		if err != nil {
			setupLog.Error(err, "unable to set up management controllers")
			os.Exit(1)
		}
		runnables = append(runnables, extra...)
	}
	// ReferencedDataController is a management-plane controller (it reconciles
	// WorkloadDeployments on project clusters and materialises companions). Gate
	// it to the management controller set so it does not collide with the cell's
	// WorkloadDeploymentReconciler.
	if enableManagementControllers {
		if err = (&controller.ReferencedDataController{}).SetupWithManager(mgr, controller.ReferencedDataControllerOptions{
			// ProjectReader is nil for single-cluster mode; the controller falls back
			// to a LocalReader. Set this to a *referenceddata.ProjectReader when the
			// Milo multicluster mode is active and cross-project reads are required.
			Reader: nil,
			// FederationClient is set when the federation hub (Karmada) is configured.
			// When non-nil, companions are materialised into the downstream
			// ns-{project-uid} namespace on the hub so Karmada can propagate them
			// to cells alongside the WorkloadDeployment. When nil, companions land
			// in the project namespace (single-cluster / dev path).
			FederationClient:    federationClient,
			PerObjectLimitBytes: serverConfig.ReferencedData.PerObjectLimitBytes,
			AggregateLimitBytes: serverConfig.ReferencedData.AggregateLimitBytes,
		}); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "ReferencedData")
			os.Exit(1)
		}
	}

	if serverConfig.WebhookServer != nil {
		if err = computev1alphawebhooks.SetupWorkloadWebhookWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create webhook", "webhook", "Workload")
			os.Exit(1)
		}
	}

	// +kubebuilder:scaffold:builder

	if err = controller.AddIndexers(ctx, mgr); err != nil {
		setupLog.Error(err, "unable to add indexers")
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
	// In multi-cluster discovery modes, the webhook resolves project clusters
	// through discoveryCache's informer. Until that informer completes its
	// initial sync, the provider hasn't seen the full set of currently-existing
	// projects and admission requests routed to this pod can be spuriously
	// denied with "cluster not found" (datum-cloud/compute#117). Gating
	// readiness on the sync lets the Service withhold this pod's endpoint
	// until that window has closed.
	if discoveryCache != nil {
		if err := mgr.AddReadyzCheck("project-discovery-synced", projectDiscoverySyncedCheck(discoveryCache)); err != nil {
			setupLog.Error(err, "unable to set up project discovery ready check")
			os.Exit(1)
		}

		// A synced discovery informer only proves the provider has seen the
		// list of currently-ready projects, not that each one's cluster
		// connection has actually been established. Require real engagement
		// too, closing the gap the discovery-synced check alone leaves open.
		if engaged != nil {
			check := projectsEngagedCheck(discoveryCache, engaged, serverConfig.Discovery.InternalServiceDiscovery)
			if err := mgr.AddReadyzCheck("projects-engaged", check); err != nil {
				setupLog.Error(err, "unable to set up project engagement ready check")
				os.Exit(1)
			}
		}
	}

	g, ctx := errgroup.WithContext(ctx)
	for _, runnable := range runnables {
		g.Go(func() error {
			return ignoreCanceled(runnable.Start(ctx))
		})
	}

	setupLog.Info("starting multicluster manager")
	g.Go(func() error {
		return ignoreCanceled(mgr.Start(ctx))
	})

	if err := g.Wait(); err != nil {
		setupLog.Error(err, "unable to start")
		os.Exit(1)
	}
}

func initializeClusterDiscovery(
	serverConfig config.WorkloadOperator,
	deploymentCluster cluster.Cluster,
	scheme *runtime.Scheme,
) (
	runnables []manager.Runnable,
	provider multicluster.Provider,
	edgeClusterName string,
	discoveryCache cache.Cache,
	err error,
) {
	runnables = append(runnables, deploymentCluster)
	switch serverConfig.Discovery.Mode {
	case multiclusterproviders.ProviderSingle:
		provider = mcsingle.New(multicluster.ClusterName(singleClusterName), deploymentCluster)
		edgeClusterName = serverConfig.Discovery.ClusterName
		if edgeClusterName == "" {
			edgeClusterName = singleClusterName
		}

	case multiclusterproviders.ProviderMilo:
		discoveryRestConfig, err := serverConfig.Discovery.DiscoveryRestConfig()
		if err != nil {
			return nil, nil, "", nil, fmt.Errorf("unable to get discovery rest config: %w", err)
		}

		projectRestConfig, err := serverConfig.Discovery.ProjectRestConfig()
		if err != nil {
			return nil, nil, "", nil, fmt.Errorf("unable to get project rest config: %w", err)
		}

		discoveryManager, err := manager.New(discoveryRestConfig, manager.Options{
			Metrics: metricsserver.Options{BindAddress: "0"},
			Cache: cache.Options{
				// Unstructured objects carry the full managedFields tree in their
				// content map, so stripping it here is especially impactful for the
				// discovery cache (#161).
				DefaultTransform: cache.TransformStripManagedFields(),
			},
			Client: client.Options{
				Cache: &client.CacheOptions{
					Unstructured: true,
				},
			},
		})
		if err != nil {
			return nil, nil, "", nil, fmt.Errorf("unable to set up overall controller manager: %w", err)
		}

		provider, err = milomulticluster.New(discoveryManager, milomulticluster.Options{
			ClusterOptions: []cluster.Option{
				func(o *cluster.Options) {
					o.Scheme = scheme
					// Applied to every per-project cluster cache. These are the
					// caches that fan in concurrently at startup, so stripping
					// managedFields here is the largest single lever on the
					// startup memory spike (#161).
					o.Cache.DefaultTransform = cache.TransformStripManagedFields()
				},
			},
			InternalServiceDiscovery: serverConfig.Discovery.InternalServiceDiscovery,
			ProjectRestConfig:        projectRestConfig,
		})
		if err != nil {
			return nil, nil, "", nil, fmt.Errorf("unable to create datum project provider: %w", err)
		}

		runnables = append(runnables, discoveryManager)
		edgeClusterName = serverConfig.Discovery.ClusterName
		discoveryCache = discoveryManager.GetCache()

	// case providers.ProviderKind:
	// 	provider = mckind.New(mckind.Options{
	// 		ClusterOptions: []cluster.Option{
	// 			func(o *cluster.Options) {
	// 				o.Scheme = scheme
	// 			},
	// 		},
	// 	})

	default:
		return nil, nil, "", nil, fmt.Errorf(
			"unsupported cluster discovery mode %s",
			serverConfig.Discovery.Mode,
		)
	}

	return runnables, provider, edgeClusterName, discoveryCache, nil
}

// projectDiscoverySyncedCheck reports ready only once the project discovery
// cache has completed its initial sync — i.e. the multicluster provider has
// observed the full set of currently-existing Project (or ProjectControlPlane)
// objects. Until then, GetCluster lookups for a project this pod hasn't heard
// about yet would be indistinguishable from a genuinely nonexistent project.
func projectDiscoverySyncedCheck(c cache.Cache) healthz.Checker {
	return func(req *http.Request) error {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()
		if !c.WaitForCacheSync(ctx) {
			return fmt.Errorf("project discovery cache has not completed its initial sync")
		}
		return nil
	}
}

// nonLeaderElectionRunnable forces a manager.Runnable into controller-runtime's
// always-running runnable group, regardless of leader-election state.
type nonLeaderElectionRunnable struct {
	manager.Runnable
}

func (nonLeaderElectionRunnable) NeedLeaderElection() bool { return false }

// engageTracker wraps a multicluster.Aware to record which cluster names have
// completed Engage, so readiness can require actual engagement rather than
// merely knowing a project exists (datum-cloud/compute#117).
type engageTracker struct {
	multicluster.Aware

	mu      sync.RWMutex
	engaged map[multicluster.ClusterName]struct{}
}

func newEngageTracker(aware multicluster.Aware) *engageTracker {
	return &engageTracker{Aware: aware, engaged: map[multicluster.ClusterName]struct{}{}}
}

func (t *engageTracker) Engage(ctx context.Context, name multicluster.ClusterName, cl cluster.Cluster) error {
	if err := t.Aware.Engage(ctx, name, cl); err != nil {
		return err
	}
	t.mu.Lock()
	t.engaged[name] = struct{}{}
	t.mu.Unlock()
	return nil
}

func (t *engageTracker) isEngaged(name multicluster.ClusterName) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.engaged[name]
	return ok
}

// conditionedObject captures just enough of a Project/ProjectControlPlane's
// shape to read its status conditions off the unstructured discovery cache.
type conditionedObject struct {
	Status struct {
		Conditions []metav1.Condition `json:"conditions"`
	} `json:"status"`
}

// projectsEngagedCheck reports ready only once every currently-ready Project
// (or ProjectControlPlane, under internal service discovery) known to the
// discovery cache has actually been engaged by the multicluster provider on
// this pod. This is what the webhook path needs: a project the provider knows
// about but hasn't finished connecting to would still fail GetCluster.
func projectsEngagedCheck(
	discoveryCache cache.Cache,
	tracker *engageTracker,
	internalServiceDiscovery bool,
) healthz.Checker {
	gvk := resourcemanagerv1alpha1.GroupVersion.WithKind("Project")
	readyCondition := "Ready"
	if internalServiceDiscovery {
		gvk = infrastructurev1alpha1.GroupVersion.WithKind("ProjectControlPlane")
		readyCondition = "ControlPlaneReady"
	}

	return func(req *http.Request) error {
		ctx, cancel := context.WithTimeout(req.Context(), 2*time.Second)
		defer cancel()

		var list unstructured.UnstructuredList
		list.SetGroupVersionKind(gvk)
		if err := discoveryCache.List(ctx, &list); err != nil {
			return fmt.Errorf("unable to list projects for engagement check: %w", err)
		}

		for _, item := range list.Items {
			var obj conditionedObject
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(item.Object, &obj); err != nil {
				continue
			}
			if !apimeta.IsStatusConditionTrue(obj.Status.Conditions, readyCondition) {
				continue
			}
			if name := multicluster.ClusterName(item.GetName()); !tracker.isEngaged(name) {
				return fmt.Errorf("project %q is ready but not yet engaged", name)
			}
		}
		return nil
	}
}

func loadServerConfig(path string) (config.WorkloadOperator, error) {
	var serverConfig config.WorkloadOperator
	var configData []byte
	if len(path) > 0 {
		var err error
		configData, err = os.ReadFile(path)
		if err != nil {
			return serverConfig, fmt.Errorf("unable to read server config from %q: %w", path, err)
		}
	}
	if err := runtime.DecodeInto(codecs.UniversalDecoder(), configData, &serverConfig); err != nil {
		return serverConfig, fmt.Errorf("unable to decode server config: %w", err)
	}
	return serverConfig, nil
}

func ignoreCanceled(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// setupManagementControllers wires the WorkloadDeploymentFederator and
// InstanceProjector onto mgr. It returns any additional Runnable objects that
// must be started alongside the main manager (the federation manager used by
// InstanceProjector). Called only when management controllers are enabled and
// a federation REST config is available.
func setupManagementControllers(mgr mcmanager.Manager, federationClient client.Client) ([]manager.Runnable, error) {
	// The federation manager provides a cached, watchable handle to the Karmada
	// federation control plane. It backs the InstanceProjector's Instance watch
	// and the WorkloadDeploymentFederator's downstream WorkloadDeployment status
	// watch. A manager.Manager embeds a cluster.Cluster, so it can be passed
	// directly anywhere a watchable federation cluster source is required.
	federationMgr, err := manager.New(federationRestConfig, manager.Options{
		Scheme:  scheme,
		Cache:   cache.Options{DefaultTransform: cache.TransformStripManagedFields()},
		Metrics: metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return nil, fmt.Errorf("federation manager: %w", err)
	}

	// The federator watches both the project WD (via the multicluster manager)
	// and the downstream Karmada WD (via the federation cluster) so that status
	// aggregated downstream by Karmada is mirrored back to the project WD
	// immediately instead of on the next informer resync.
	federator := &controller.WorkloadDeploymentFederator{
		FederationClient:  federationClient,
		FederationCluster: federationMgr,
	}
	if err := federator.SetupWithManager(mgr); err != nil {
		return nil, fmt.Errorf("WorkloadDeploymentFederator: %w", err)
	}

	// InstanceProjector runs in the management plane, watches Instances written
	// back by POP-cell operators to the Karmada federation control plane, and
	// projects them into the corresponding project namespaces via the multicluster manager.
	if err = (&controller.InstanceProjector{
		FederationClient: federationClient,
		MCManager:        mgr,
	}).SetupWithManager(federationMgr); err != nil {
		return nil, fmt.Errorf("InstanceProjector: %w", err)
	}

	return []manager.Runnable{federationMgr}, nil
}

// computeWatchProviderClaims reports whether the direct ResourceClaim watch
// should be registered. The watch needs the quota CRD, which lives only on the
// Milo project control planes, so it is Milo-mode only; registering it in
// single/cluster mode would crash the manager against a cell that has no quota
// CRD (#171). This gates the watch only — claim writes (REST calls to the
// owning project's quota API) run in any mode.
func computeWatchProviderClaims(mode multiclusterproviders.Provider) bool {
	return mode == multiclusterproviders.ProviderMilo
}
