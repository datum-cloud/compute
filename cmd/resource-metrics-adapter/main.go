// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	corev1listers "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/metadata/metadatainformer"
	"k8s.io/client-go/metadata/metadatalister"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/component-base/logs"
	"k8s.io/klog/v2"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsapi "sigs.k8s.io/metrics-server/pkg/api"

	"go.datum.net/compute/internal/resourcemetrics"
)

type stringList []string

func (l *stringList) String() string {
	return strings.Join(*l, ",")
}

func (l *stringList) Set(value string) error {
	*l = append(*l, value)
	return nil
}

func (l *stringList) Type() string {
	return "stringList"
}

func main() {
	logs.InitLogs()
	defer logs.FlushLogs()

	var sourceURLs stringList
	var sourceInsecureSkipTLSVerify bool
	var sourceBearerTokenFile string

	recommendedOptions := genericoptions.NewRecommendedOptions("", metricsapi.Codecs.LegacyCodec(metricsv1beta1.SchemeGroupVersion))
	recommendedOptions.Etcd = nil
	recommendedOptions.SecureServing.BindPort = 8443
	recommendedOptions.SecureServing.ServerCert.CertKey.CertFile = "/certs/tls.crt"
	recommendedOptions.SecureServing.ServerCert.CertKey.KeyFile = "/certs/tls.key"

	pflag.Var(&sourceURLs, "source-url", "metrics.k8s.io-compatible source URL. Can be specified more than once.")
	pflag.BoolVar(&sourceInsecureSkipTLSVerify, "source-insecure-skip-tls-verify", false, "Skip TLS certificate verification for metric sources.")
	pflag.StringVar(&sourceBearerTokenFile, "source-bearer-token-file", "", "Path to a bearer token file used when querying metric sources.")
	recommendedOptions.AddFlags(pflag.CommandLine)
	pflag.Parse()

	backend, err := buildBackend(sourceURLs, sourceInsecureSkipTLSVerify, sourceBearerTokenFile)
	if err != nil {
		klog.ErrorS(err, "failed building metrics backend")
		os.Exit(1)
	}

	serverConfig := genericapiserver.NewRecommendedConfig(metricsapi.Codecs)
	if err := recommendedOptions.ApplyTo(serverConfig); err != nil {
		klog.ErrorS(err, "failed applying apiserver options")
		os.Exit(1)
	}

	genericServer, err := serverConfig.Complete().New("compute-resource-metrics-adapter", genericapiserver.NewEmptyDelegate())
	if err != nil {
		klog.ErrorS(err, "failed creating apiserver")
		os.Exit(1)
	}

	podInformerFactory, podMetadataLister, nodeInformerFactory, nodeLister, err := informersFor(serverConfig.ClientConfig)
	if err != nil {
		klog.ErrorS(err, "failed creating informers")
		os.Exit(1)
	}

	if err := metricsapi.Install(resourcemetrics.NewMetricsGetter(backend), podMetadataLister, nodeLister, genericServer, nil); err != nil {
		klog.ErrorS(err, "failed installing metrics API")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	stopCh := ctx.Done()
	podInformerFactory.Start(stopCh)
	nodeInformerFactory.Start(stopCh)
	if !cache.WaitForCacheSync(stopCh, podInformerFactory.ForResource(corev1.SchemeGroupVersion.WithResource("pods")).Informer().HasSynced, nodeInformerFactory.Core().V1().Nodes().Informer().HasSynced) {
		klog.ErrorS(errors.New("informer cache sync failed"), "failed starting adapter")
		os.Exit(1)
	}

	klog.InfoS("starting resource metrics adapter")
	if err := genericServer.PrepareRun().RunWithContext(ctx); err != nil {
		klog.ErrorS(err, "resource metrics adapter exited")
		os.Exit(1)
	}
}

func buildBackend(sourceURLs []string, sourceInsecureSkipTLSVerify bool, sourceBearerTokenFile string) (resourcemetrics.Backend, error) {
	var backends []resourcemetrics.Backend
	for _, sourceURL := range sourceURLs {
		metricBackend, err := resourcemetrics.NewMetricsAPIBackendForURL(sourceURL, resourcemetrics.MetricsAPIBackendOptions{
			InsecureSkipTLSVerify: sourceInsecureSkipTLSVerify,
			BearerTokenFile:       sourceBearerTokenFile,
		})
		if err != nil {
			return nil, fmt.Errorf("invalid source URL %q: %w", sourceURL, err)
		}
		backends = append(backends, metricBackend)
	}
	if len(backends) == 0 {
		return resourcemetrics.EmptyBackend{}, nil
	}
	return resourcemetrics.NewCompositeBackend(backends...), nil
}

func informersFor(restConfig *rest.Config) (metadatainformer.SharedInformerFactory, cache.GenericLister, informers.SharedInformerFactory, corev1listers.NodeLister, error) {
	metadataClient, err := metadata.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	kubeClient, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, nil, nil, nil, err
	}

	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	podInformerFactory := metadatainformer.NewSharedInformerFactory(metadataClient, 10*time.Minute)
	podInformer := podInformerFactory.ForResource(podGVR)
	nodeInformerFactory := informers.NewSharedInformerFactory(kubeClient, 10*time.Minute)
	podLister := metadatalister.New(podInformer.Informer().GetIndexer(), podGVR)
	return podInformerFactory, metadatalister.NewRuntimeObjectShim(podLister), nodeInformerFactory, nodeInformerFactory.Core().V1().Nodes().Lister(), nil
}
