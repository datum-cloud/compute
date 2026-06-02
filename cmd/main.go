// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/labels"
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
	karmadaworkv1alpha2 "github.com/karmada-io/api/work/v1alpha2"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/config"
	"go.datum.net/compute/internal/controller"
	"go.datum.net/compute/internal/features"
	quotametrics "go.datum.net/compute/internal/quota"
	computewebhook "go.datum.net/compute/internal/webhook"
	computev1alphawebhooks "go.datum.net/compute/internal/webhook/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
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
	utilruntime.Must(karmadaworkv1alpha2.Install(scheme))

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
			"Example: --feature-gates=NetworkingIntegration=false. "+
			"Available features: NetworkingIntegration (default=true).")

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
	})
	if err != nil {
		setupLog.Error(err, "failed creating local cluster")
		os.Exit(1)
	}

	runnables, provider, edgeClusterName, err := initializeClusterDiscovery(
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

	mgr, err := mcmanager.New(cfg, provider, ctrl.Options{
		Scheme:                  scheme,
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
		instanceReconciler := &controller.InstanceReconciler{FederationClient: federationClient}
		err = instanceReconciler.SetupWithManager(
			mgr,
			quotaRestConfig,
			controller.NewSingleModeProjectID(mgr),
			controller.NewSingleModeProjectNamespace(mgr),
			edgeClusterName,
			clusterNameForProject,
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
) (runnables []manager.Runnable, provider multicluster.Provider, edgeClusterName string, err error) {
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
			return nil, nil, "", fmt.Errorf("unable to get discovery rest config: %w", err)
		}

		projectRestConfig, err := serverConfig.Discovery.ProjectRestConfig()
		if err != nil {
			return nil, nil, "", fmt.Errorf("unable to get project rest config: %w", err)
		}

		discoveryManager, err := manager.New(discoveryRestConfig, manager.Options{
			Metrics: metricsserver.Options{BindAddress: "0"},
			Client: client.Options{
				Cache: &client.CacheOptions{
					Unstructured: true,
				},
			},
		})
		if err != nil {
			return nil, nil, "", fmt.Errorf("unable to set up overall controller manager: %w", err)
		}

		provider, err = milomulticluster.New(discoveryManager, milomulticluster.Options{
			ClusterOptions: []cluster.Option{
				func(o *cluster.Options) {
					o.Scheme = scheme
				},
			},
			InternalServiceDiscovery: serverConfig.Discovery.InternalServiceDiscovery,
			ProjectRestConfig:        projectRestConfig,
		})
		if err != nil {
			return nil, nil, "", fmt.Errorf("unable to create datum project provider: %w", err)
		}

		runnables = append(runnables, discoveryManager)
		edgeClusterName = serverConfig.Discovery.ClusterName

	// case providers.ProviderKind:
	// 	provider = mckind.New(mckind.Options{
	// 		ClusterOptions: []cluster.Option{
	// 			func(o *cluster.Options) {
	// 				o.Scheme = scheme
	// 			},
	// 		},
	// 	})

	default:
		return nil, nil, "", fmt.Errorf(
			"unsupported cluster discovery mode %s",
			serverConfig.Discovery.Mode,
		)
	}

	return runnables, provider, edgeClusterName, nil
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
	// companionLabelSelector scopes the federation manager's ConfigMap and
	// Secret informer cache to referenced-data companions only. Without this,
	// For(&corev1.ConfigMap{}) in CompanionGCReconciler would establish a
	// cluster-wide ConfigMap+Secret informer that caches every object on the
	// Karmada hub — the same OOM pattern that killed the cell CompanionGCReconciler.
	// The label CACHE scope (not the predicate) is the correct OOM guard:
	// predicates filter events, not cache contents.
	companionLabelSelector := labels.SelectorFromSet(labels.Set{
		computev1alpha.ReferencedDataLabel: computev1alpha.ReferencedDataLabelValue,
	})

	// The federation manager provides a cached, watchable handle to the Karmada
	// federation control plane. It backs the InstanceProjector's Instance watch,
	// the WorkloadDeploymentFederator's downstream WorkloadDeployment status watch,
	// and the CompanionGCReconciler. A manager.Manager embeds a cluster.Cluster, so
	// it can be passed directly anywhere a watchable federation cluster source is
	// required.
	federationMgr, err := manager.New(federationRestConfig, manager.Options{
		Scheme:  scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		Cache: cache.Options{
			// Scope ConfigMap and Secret informers to referenced-data companions.
			// CompanionGCReconciler is the only consumer on federationMgr that
			// reads these types; nothing else (InstanceProjector, OrphanRBReconciler)
			// needs non-companion CMs or Secrets from the cache.
			ByObject: map[client.Object]cache.ByObject{
				&corev1.ConfigMap{}: {Label: companionLabelSelector},
				&corev1.Secret{}:    {Label: companionLabelSelector},
			},
		},
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

	// OrphanRBReconciler sweeps Karmada ResourceBindings whose hub companion is
	// gone, ensuring Works and cell copies are cleaned up even when Karmada's
	// event-driven cascade misses the companion-deletion event (e.g. PP deleted
	// before binding-controller reconcile completed). Runs on the hub federation
	// manager alongside InstanceProjector.
	if err = controller.SetupOrphanRBWithManager(federationMgr, federationClient); err != nil {
		return nil, fmt.Errorf("OrphanRBReconciler: %w", err)
	}

	// CompanionGCReconciler is a level-triggered backstop for stranded hub
	// companions: labeled ConfigMaps/Secrets whose referenced-by annotation
	// points at WDs that no longer exist on the hub. On each reconcile it
	// checks all referrer WDs in the hub namespace; if all are absent the
	// companion and its ResourceBinding are deleted, driving the Karmada
	// cascade to clean up Works and cell copies permanently.
	if err = controller.SetupCompanionGCWithManager(federationMgr, federationClient); err != nil {
		return nil, fmt.Errorf("CompanionGCReconciler: %w", err)
	}

	return []manager.Runnable{federationMgr}, nil
}
