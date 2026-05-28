// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

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

	corev1 "k8s.io/api/core/v1"
	karmadaclusterv1alpha1 "github.com/karmada-io/api/cluster/v1alpha1"
	karmadapolicyv1alpha1 "github.com/karmada-io/api/policy/v1alpha1"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/config"
	"go.datum.net/compute/internal/controller"
	computewebhook "go.datum.net/compute/internal/webhook"
	computev1alphawebhooks "go.datum.net/compute/internal/webhook/v1alpha"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	"go.miloapis.com/milo/pkg/downstreamclient"
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

	// upstreamRestConfig holds the REST config for the upstream federation control
	// plane (Karmada). It is populated from --upstream-kubeconfig when set, and
	// is nil when the flag is omitted.
	upstreamRestConfig *rest.Config
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
	var upstreamKubeconfig string
	var upstreamContext string
	var enableManagementControllers bool
	var enableCellControllers bool

	flag.StringVar(&probeAddr, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")
	flag.BoolVar(&enableLeaderElection, "leader-elect", false,
		"Enable leader election for controller manager. "+
			"Enabling this will ensure there is only one active controller manager.")
	flag.StringVar(&leaderElectionNamespace, "leader-elect-namespace", "", "The namespace to use for leader election.")
	flag.StringVar(&upstreamKubeconfig, "upstream-kubeconfig", "",
		"Path to the kubeconfig file for the upstream federation control plane (Karmada). "+
			"When omitted, federation features are disabled.")
	flag.StringVar(&upstreamContext, "upstream-context", "",
		"Context to use from the upstream kubeconfig. When omitted, the current context is used.")
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
	// When the flag is omitted, upstreamRestConfig remains nil and federation
	// features will be skipped at controller setup time.
	if upstreamKubeconfig != "" {
		loader := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			&clientcmd.ClientConfigLoadingRules{ExplicitPath: upstreamKubeconfig},
			&clientcmd.ConfigOverrides{CurrentContext: upstreamContext},
		)
		var err error
		upstreamRestConfig, err = loader.ClientConfig()
		if err != nil {
			setupLog.Error(err, "unable to load upstream kubeconfig", "path", upstreamKubeconfig)
			os.Exit(1)
		}
		setupLog.Info("upstream kubeconfig loaded", "path", upstreamKubeconfig)
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
	} else {
		setupLog.Info("quota REST config not configured; quota enforcement disabled")
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

	// Build a single downstream client shared across all controllers that need
	// to read or write to the downstream control plane. Nil when federation is disabled.
	var downstreamClient client.Client
	if upstreamRestConfig != nil {
		downstreamClient, err = client.New(upstreamRestConfig, client.Options{Scheme: scheme})
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
		projectIDForInstance := func(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) string {
			cl, err := mgr.GetCluster(ctx, clusterName)
			if err != nil {
				setupLog.Error(err, "projectIDForInstance: failed getting cluster",
					"clusterName", clusterName)
				return ""
			}
			var ns corev1.Namespace
			getCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := cl.GetAPIReader().Get(getCtx, client.ObjectKey{Name: instance.Namespace}, &ns); err != nil {
				setupLog.Error(err, "projectIDForInstance: failed reading namespace",
					"namespace", instance.Namespace)
				return ""
			}
			encoded := ns.Labels[downstreamclient.UpstreamOwnerClusterNameLabel]
			if encoded == "" {
				setupLog.Info("projectIDForInstance: upstream-cluster-name label missing or empty",
					"namespace", instance.Namespace)
				return ""
			}
			projectID := strings.TrimPrefix(encoded, "cluster-")
			projectID = strings.ReplaceAll(projectID, "_", "/")
			return projectID
		}
		// projectNamespaceForInstance reads the upstream-namespace label from the
		// edge namespace (e.g. "ns-efdf8ca1-...") to find the in-project namespace
		// (e.g. "default") where ResourceClaims must be created in the project
		// control plane. The edge namespace name itself does not exist there.
		projectNamespaceForInstance := func(ctx context.Context, clusterName multicluster.ClusterName, instance *computev1alpha.Instance) string {
			cl, err := mgr.GetCluster(ctx, clusterName)
			if err != nil {
				setupLog.Error(err, "projectNamespaceForInstance: failed getting cluster",
					"clusterName", clusterName)
				return ""
			}
			var ns corev1.Namespace
			getCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			if err := cl.GetAPIReader().Get(getCtx, client.ObjectKey{Name: instance.Namespace}, &ns); err != nil {
				setupLog.Error(err, "projectNamespaceForInstance: failed reading namespace",
					"namespace", instance.Namespace)
				return ""
			}
			projectNS := ns.Labels[downstreamclient.UpstreamOwnerNamespaceLabel]
			if projectNS == "" {
				setupLog.Info("projectNamespaceForInstance: upstream-namespace label missing or empty",
					"namespace", instance.Namespace)
				return ""
			}
			return projectNS
		}
		clusterNameForProject := func(_ string) multicluster.ClusterName {
			return multicluster.ClusterName(singleClusterName)
		}
		instanceReconciler := &controller.InstanceReconciler{UpstreamClient: downstreamClient}
		if err = instanceReconciler.SetupWithManager(mgr, quotaRestConfig, projectIDForInstance, projectNamespaceForInstance, edgeClusterName, clusterNameForProject); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "Instance")
			os.Exit(1)
		}
	}

	// WorkloadDeploymentFederator and InstanceProjector are management-plane
	// controllers that run on the control-plane cluster. They require a downstream
	// control plane to be configured (--downstream-kubeconfig provided).
	if enableManagementControllers && upstreamRestConfig != nil {
		federator := &controller.WorkloadDeploymentFederator{UpstreamClient: downstreamClient}
		if err = federator.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "unable to create controller", "controller", "WorkloadDeploymentFederator")
			os.Exit(1)
		}

		// InstanceProjector: runs in the Control Plane Cell, watches Instances
		// written back to the downstream control plane by POP-cell operators, and
		// projects them into the corresponding project namespaces via the
		// multicluster manager.
		downstreamMgr, err := manager.New(upstreamRestConfig, manager.Options{
			Scheme:  scheme,
			Metrics: metricsserver.Options{BindAddress: "0"},
		})
		if err != nil {
			setupLog.Error(err, "unable to create downstream manager for InstanceProjector")
			os.Exit(1)
		}
		if err = (&controller.InstanceProjector{
			UpstreamClient: downstreamClient,
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
