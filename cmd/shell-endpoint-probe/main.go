// SPDX-License-Identifier: AGPL-3.0-only

// Command shell-endpoint-probe runs beside a cell's tunnel endpoint and serves
// its readiness: ready only while a client can reach the endpoint through the
// relays the paired shell agent publishes. The agent claims sessions only
// while its endpoint is ready, so an endpoint that lost its relay, or is homed
// on one clients are not told about, stops taking sessions instead of failing
// every client's dial.
package main

import (
	"context"
	"errors"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"go.datum.net/compute/internal/endpointprobe"
)

func main() {
	var (
		cfg       endpointprobe.Config
		relayURLs string
		listen    string
	)
	flag.StringVar(&listen, "listen", ":8082", "Address to serve the readiness check on.")
	flag.StringVar(&cfg.KeyFile, "key-file", "", "The endpoint's raw 32-byte identity key.")
	flag.StringVar(&relayURLs, "relay-urls", os.Getenv("DATUM_CONNECT_RELAY_URLS"),
		"Comma-separated relays the paired agent publishes.")
	flag.StringVar(&cfg.Target, "target", "", "host:port the endpoint proxies to its agent.")
	flag.DurationVar(&cfg.Interval, "interval", 10*time.Second, "How often the endpoint is probed.")
	flag.DurationVar(&cfg.Timeout, "timeout", 8*time.Second, "How long one probe may take.")
	zapOpts := zap.Options{}
	zapOpts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&zapOpts)))
	logger := ctrl.Log.WithName("shell-endpoint-probe")

	for _, u := range strings.Split(relayURLs, ",") {
		if u = strings.TrimSpace(u); u != "" {
			cfg.RelayURLs = append(cfg.RelayURLs, u)
		}
	}
	prober, err := endpointprobe.New(cfg)
	if err != nil {
		logger.Error(err, "invalid configuration")
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(ctrl.LoggerInto(context.Background(), logger), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	mux := http.NewServeMux()
	mux.Handle("/readyz", prober)
	server := &http.Server{Addr: listen, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	go func() { _ = prober.Run(ctx) }()

	logger.Info("probing endpoint", "relays", cfg.RelayURLs, "target", cfg.Target)
	if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		logger.Error(err, "serve readiness")
		os.Exit(1)
	}
}
