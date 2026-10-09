// SPDX-License-Identifier: AGPL-3.0-only

// Command shell-agent serves instance shell sessions in a compute cell. Each
// replica pairs by ordinal with a tunnel endpoint, which is the only way a
// client reaches it: the endpoint proxies one TCP target, this agent's exec
// port. The agent claims sessions delivered to the cell, runs their commands
// through the cell apiserver, and writes their status on the cell's copies,
// which Karmada reflects back to the hub. It needs no hub credential.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	"go.datum.net/compute/internal/shellagent"
	"go.datum.net/compute/internal/shelltrace"
)

var setupLog = ctrl.Log.WithName("setup")

type options struct {
	listen            string
	probeAddr         string
	metricsAddr       string
	cell              string
	target            string
	relayURLs         string
	managedBy         string
	endpointPodPrefix string
	featureGates      string
	drainTimeout      time.Duration
	killGrace         time.Duration
	pingInterval      time.Duration
	pongTimeout       time.Duration
}

func main() {
	defaults := shellagent.DefaultConfig()
	var opts options
	flag.StringVar(&opts.listen, "listen", ":7777", "Address to serve session connections on.")
	flag.StringVar(&opts.probeAddr, "health-probe-bind-address", ":8081", "Address the probe endpoint binds to.")
	flag.StringVar(&opts.metricsAddr, "metrics-bind-address", ":8080",
		"Address the metrics endpoint binds to, or 0 to disable it.")
	flag.StringVar(&opts.cell, "cell", os.Getenv("CELL_NAME"),
		"Name of the cell this agent serves, on its metrics and logs.")
	flag.StringVar(&opts.target, "target", "",
		"host:port the paired tunnel endpoint proxies to this agent, published as the session's target.")
	flag.StringVar(&opts.relayURLs, "relay-urls", os.Getenv("DATUM_CONNECT_RELAY_URLS"),
		"Comma-separated relays the paired tunnel endpoint uses, nearest first.")
	flag.StringVar(&opts.managedBy, "managed-by", strings.Join(defaults.ManagedBy, ","),
		"Comma-separated managed-by label values of instance pods that may take sessions.")
	flag.StringVar(&opts.endpointPodPrefix, "endpoint-pod-prefix", "exec-endpoint",
		"Name of the tunnel endpoint StatefulSet; the paired pod is <prefix>-<ordinal>.")
	flag.StringVar(&opts.featureGates, "feature-gates", "",
		"Feature gates, for example InstanceConsoleSessions=true.")
	flag.DurationVar(&opts.drainTimeout, "drain-timeout", defaults.DrainTimeout,
		"How long connected sessions may continue after the agent is asked to stop.")
	flag.DurationVar(&opts.killGrace, "kill-grace", defaults.KillGrace,
		"Time between SIGHUP and SIGKILL when a session's processes are stopped.")
	flag.DurationVar(&opts.pingInterval, "ping-interval", defaults.PingInterval,
		"How often the agent pings a connected client.")
	flag.DurationVar(&opts.pongTimeout, "pong-timeout", defaults.PongTimeout,
		"How long a connected client may send nothing, not even a pong, before its session ends as Disconnected.")
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))

	if err := run(opts, defaults); err != nil {
		setupLog.Error(err, "shell agent failed")
		os.Exit(1)
	}
}

func run(opts options, cfg shellagent.Config) error {
	if opts.featureGates != "" {
		if err := features.MutableFeatureGate.Set(opts.featureGates); err != nil {
			return fmt.Errorf("parse feature gates: %w", err)
		}
	}
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)

	if !features.FeatureGate.Enabled(features.InstanceConsoleSessions) {
		setupLog.Info("the InstanceConsoleSessions feature gate is off; not serving sessions")
		<-signals
		return nil
	}
	shutdownTraces, err := shelltrace.Init(context.Background(), "compute-shell-agent")
	if err != nil {
		setupLog.Error(err, "unable to configure shell tracing")
	} else {
		defer func() {
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := shutdownTraces(flushCtx); err != nil {
				setupLog.Error(err, "unable to flush shell traces")
			}
		}()
	}

	podName, namespace := os.Getenv("POD_NAME"), os.Getenv("POD_NAMESPACE")
	ordinal, err := ordinalOf(podName)
	if err != nil || namespace == "" {
		return fmt.Errorf("POD_NAME must be a StatefulSet pod name and POD_NAMESPACE must be set: %w", err)
	}
	cfg.Namespace = namespace
	cfg.Ordinal = ordinal
	cfg.Cell = opts.cell
	cfg.Agent = podName
	cfg.EndpointPodName = fmt.Sprintf("%s-%d", opts.endpointPodPrefix, ordinal)
	cfg.Target = opts.target
	cfg.RelayURLs = splitList(opts.relayURLs)
	cfg.ManagedBy = splitList(opts.managedBy)
	cfg.DrainTimeout = opts.drainTimeout
	cfg.KillGrace = opts.killGrace
	cfg.PingInterval = opts.pingInterval
	cfg.PongTimeout = opts.pongTimeout

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(computev1alpha.AddToScheme(scheme))

	cellConfig := ctrl.GetConfigOrDie()

	// Every line the agent logs names the cell and agent, so one activity
	// event's cell annotation leads to this agent's logs.
	logger := ctrl.Log.WithName("shell-agent").WithValues("cell", cfg.Cell, "agent", cfg.Agent)
	mgr, err := ctrl.NewManager(cellConfig, ctrl.Options{
		Scheme:                 scheme,
		Logger:                 logger,
		Metrics:                metricsserver.Options{BindAddress: opts.metricsAddr},
		HealthProbeBindAddress: opts.probeAddr,
	})
	if err != nil {
		return fmt.Errorf("create manager: %w", err)
	}
	cellClient, err := client.New(cellConfig, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	executor, err := shellagent.NewAPIServer(cellConfig)
	if err != nil {
		return fmt.Errorf("configure pod exec: %w", err)
	}

	incarnation := podName + "/" + strconv.FormatInt(time.Now().UnixNano(), 36)
	agent, err := shellagent.New(cfg, mgr.GetClient(), cellClient, executor, incarnation)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctrl.LoggerInto(context.Background(), logger))
	defer cancel()
	if err := agent.EnsureIdentity(ctx); err != nil {
		return err
	}
	if err := agent.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("set up controller: %w", err)
	}

	server := &http.Server{Addr: opts.listen, Handler: agent.Handler(ctx), ReadHeaderTimeout: 10 * time.Second}
	for _, r := range []manager.RunnableFunc{
		func(ctx context.Context) error {
			go func() {
				<-ctx.Done()
				_ = server.Close()
			}()
			if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
				return err
			}
			return nil
		},
		agent.RunLiveness,
		agent.RunSweeper,
		agent.RunKeyRotation,
	} {
		if err := mgr.Add(r); err != nil {
			return err
		}
	}
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return err
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		return err
	}

	go func() {
		<-signals
		setupLog.Info("draining", "timeout", cfg.DrainTimeout)
		agent.Drain(ctx)
		setupLog.Info("drained")
		cancel()
		<-signals
		os.Exit(1)
	}()

	setupLog.Info("starting shell agent", "cell", cfg.Cell, "agent", cfg.Agent,
		"endpointID", agent.EndpointID(), "target", cfg.Target, "metrics", opts.metricsAddr)
	return mgr.Start(ctx)
}

func ordinalOf(podName string) (int, error) {
	i := strings.LastIndex(podName, "-")
	if i < 0 {
		return 0, fmt.Errorf("pod name %q has no ordinal", podName)
	}
	return strconv.Atoi(podName[i+1:])
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
