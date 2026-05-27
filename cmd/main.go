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
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ctrl "sigs.k8s.io/controller-runtime"
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
	computewebhook "go.datum.net/compute/internal/webhook"
	computev1alphawebhooks "go.datum.net/compute/internal/webhook/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	multiclusterproviders "go.miloapis.com/milo/pkg/multicluster-runtime"
	milomulticluster "go.miloapis.com/milo/pkg/multicluster-runtime/milo"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
	codecs   = serializer.NewCodecFactory(scheme, serializer.EnableStrict)

	// Build metadata, set via -ldflags at build time. See Dockerfile.
	version      = "dev"
	gitCommit    = "unknown"
	gitTreeState = "unknown"
	buildDate    = "unknown"

	// downstreamRestConfig holds the REST config for the downstream control plane.
	// It is populated from --downstream-kubeconfig when set, and is nil when the
	// flag is omitted (e.g. in non-federation deployments).
	downstreamRestConfig *rest.Config
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

func main() {

	var enableLeaderElection bool
	var leaderElectionNamespace string
	var probeAddr string
	var serverConfigFile string
	var downstreamKubeconfig string
	var downstreamContext string
	var enableManagementControllers bool
	var enableCellControllers bool

	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&leaderElectionNamespace, "leader-elect-namespace", "", "The namespace to use for leader election.")
	flag.StringVar(&downstreamKubeconfig, "downstream-kubeconfig", "",
		"Path to the kubeconfig file for the downstream control plane. "+
			"When omitted, downstream federation features are disabled.")
	flag.StringVar(&downstreamContext, "downstream-context", "",
		"Context to use from the downstream kubeconfig. When omitted, the current context is used.")
	flag.BoolVar(&enableManagementControllers, "enable-management-controllers", false,
		"Enable management-plane controllers (WorkloadDeploymentFederator, InstanceProjector).")
	flag.BoolVar(&enableCellControllers, "enable-cell-controllers", false,
		"Enable cell controllers (WorkloadDeploymentReconciler, InstanceReconciler).")

	opts := zap.Options{
		Development: true,
	}

	flag.StringVar(&serverConfigFile, "server-config", "", "path to the server config file")

	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Load the downstream REST config when --downstream-kubeconfig is provided.
	// When the flag is omitted, downstreamRestConfig remains nil and federation
	// features will be skipped at controller setup time.
	if downstreamKubeconfig != "" {
		loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: downstreamKubeconfig},
			&clientcmd.ConfigOverrides{CurrentContext: downstreamContext},
		)
		var err error
		downstreamRestConfig, err = loader.ClientConfig()
		if err != nil {
			setupLog.Error(err, "unable to load downstream kubeconfig", "path", downstreamKubeconfig)
			os.Exit(1)
		}
		setupLog.Info("downstream kubeconfig loaded", "path", downstreamKubeconfig)
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

	cfg := ctrl.GetConfigOrDie()

	deploymentCluster, err := cluster.New(cfg, func(o *cluster.Options) {
		o.Scheme = scheme
	})
	if err != nil {
		setupLog.Error(err, "failed creating local cluster")
		os.Exit(1)
	}

	runnables, provider, projectRestConfig, edgeClusterName, err := initializeClusterDiscovery(
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

	// Build a single downstream client shared across all controllers that need
	// to read or write to the downstream control plane. Nil when federation is disabled.
	var downstreamClient client.Client
	if downstreamRestConfig != nil {
		downstreamClient, err = client.New(downstreamRestConfig, client.Options{Scheme: scheme})
		if err != nil {
			setupLog.Error(err, "unable to create downstream client")
			os.Exit(1)
		}
	}

	if enableCellControllers {
		if err = (&controller.WorkloadDeploymentReconciler{}).SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "WorkloadDeployment")
			os.Exit(1)
		}
	}

	if enableCellControllers {
		instanceReconciler := &controller.InstanceReconciler{DownstreamClient: downstreamClient}
		if err = instanceReconciler.SetupWithManager(mgr, projectRestConfig, edgeClusterName); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Instance")
			os.Exit(1)
		}
	}

	// WorkloadDeploymentFederator and InstanceProjector are management-plane
	// controllers that run on the control-plane cluster. They require a downstream
	// control plane to be configured (--downstream-kubeconfig provided).
	if enableManagementControllers && downstreamRestConfig != nil {
		federator := &controller.WorkloadDeploymentFederator{DownstreamClient: downstreamClient}
		if err = federator.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "WorkloadDeploymentFederator")
			os.Exit(1)
		}

		// InstanceProjector: runs in the Control Plane Cell, watches Instances
		// written back to the downstream control plane by POP-cell operators, and
		// projects them into the corresponding project namespaces via the
		// multicluster manager.
		downstreamMgr, err := manager.New(downstreamRestConfig, manager.Options{
			Scheme:  scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		if err != nil {
			setupLog.Error(err, "unable to create downstream manager for InstanceProjector")
			os.Exit(1)
		}
		if err = (&controller.InstanceProjector{
			DownstreamClient: downstreamClient,
			MCManager:        mgr,
		}).SetupWithManager(downstreamMgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "InstanceProjector")
			os.Exit(1)
		}
		runnables = append(runnables, downstreamMgr)
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

	setupLog.Info("starting cluster discovery provider")
	g.Go(func() error {
		return ignoreCanceled(provider.Run(ctx, mgr))
	})

	setupLog.Info("starting multicluster manager")
	g.Go(func() error {
		return ignoreCanceled(mgr.Start(ctx))
	})

	if err := g.Wait(); err != nil {
		setupLog.Error(err, "unable to start")
		os.Exit(1)
	}
}

type runnableProvider interface {
	multicluster.Provider
	Run(context.Context, mcmanager.Manager) error
}

// Needed until we contribute the patch in the following PR again (need to sign CLA):
//
//	See: https://github.com/kubernetes-sigs/multicluster-runtime/pull/18
type wrappedSingleClusterProvider struct {
	multicluster.Provider
	cluster cluster.Cluster
}

func (p *wrappedSingleClusterProvider) Run(ctx context.Context, mgr mcmanager.Manager) error {
	if err := mgr.Engage(ctx, "single", p.cluster); err != nil {
		return err
	}
	return p.Provider.(runnableProvider).Run(ctx, mgr)
}

func initializeClusterDiscovery(
	serverConfig config.WorkloadOperator,
	deploymentCluster cluster.Cluster,
	scheme *runtime.Scheme,
) (runnables []manager.Runnable, provider runnableProvider,
	projectRestConfig *rest.Config, edgeClusterName string, err error) {
	runnables = append(runnables, deploymentCluster)
	switch serverConfig.Discovery.Mode {
	case multiclusterproviders.ProviderSingle:
		provider = &wrappedSingleClusterProvider{
			Provider: mcsingle.New("single", deploymentCluster),
			cluster:  deploymentCluster,
		}

	case multiclusterproviders.ProviderMilo:
		discoveryRestConfig, err := serverConfig.Discovery.DiscoveryRestConfig()
		if err != nil {
			return nil, nil, nil, "", fmt.Errorf("unable to get discovery rest config: %w", err)
		}

		projectRestConfig, err = serverConfig.Discovery.ProjectRestConfig()
		if err != nil {
			return nil, nil, nil, "", fmt.Errorf("unable to get project rest config: %w", err)
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
			return nil, nil, nil, "", fmt.Errorf("unable to set up overall controller manager: %w", err)
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
			return nil, nil, nil, "", fmt.Errorf("unable to create datum project provider: %w", err)
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
		return nil, nil, nil, "", fmt.Errorf(
			"unsupported cluster discovery mode %s",
			serverConfig.Discovery.Mode,
		)
	}

	return runnables, provider, projectRestConfig, edgeClusterName, nil
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
