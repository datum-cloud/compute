// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
)

const (
	schemeHTTP  = "http"
	schemeHTTPS = "https"

	// shutdownTimeout bounds how long in-flight requests get to finish once the
	// process is asked to stop. It sits inside the pod's termination grace
	// period, so the kubelet never has to kill a server that is still draining.
	shutdownTimeout = 20 * time.Second
)

// tlsFiles names the serving certificate and its key. Both empty means plain
// http, which is what local development and the e2e environment run.
//
// The assistant refuses to send a caller's bearer token over anything but
// https, so a deployment the assistant calls must set both.
type tlsFiles struct {
	certFile string
	keyFile  string
}

// validate rejects a half-configured pair. One file without the other is a
// deployment mistake, and serving plain http in answer to it would put bearer
// tokens on the wire in the clear while looking healthy.
func (f tlsFiles) validate() error {
	switch {
	case f.certFile != "" && f.keyFile == "":
		return errors.New("--tls-cert-file is set but --tls-key-file is not: " +
			"set both to serve https, or neither to serve http")
	case f.certFile == "" && f.keyFile != "":
		return errors.New("--tls-key-file is set but --tls-cert-file is not: " +
			"set both to serve https, or neither to serve http")
	}
	return nil
}

func (f tlsFiles) enabled() bool {
	return f.certFile != "" && f.keyFile != ""
}

// scheme is the URL scheme the server answers on.
func (f tlsFiles) scheme() string {
	if f.enabled() {
		return schemeHTTPS
	}
	return schemeHTTP
}

// serve answers on ln until ctx is cancelled, then drains in-flight requests.
// A clean shutdown returns nil.
//
// With a certificate it serves https and nothing else. The certificate is read
// through a watcher rather than once at startup: the cert-manager CSI driver
// rewrites the files in place on renewal, and a server that loaded them once
// would present an expired certificate until its next restart.
func serve(ctx context.Context, ln net.Listener, handler http.Handler, files tlsFiles) error {
	if err := files.validate(); err != nil {
		return err
	}

	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	// Cancelled on every return path, so the watcher never outlives the server.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Buffered for both goroutines: whichever loses the race to report must
	// not block forever on a channel nobody reads any more.
	failed := make(chan error, 2)

	if files.enabled() {
		watcher, err := certwatcher.New(files.certFile, files.keyFile)
		if err != nil {
			return fmt.Errorf("loading TLS certificate %s and key %s: %w",
				files.certFile, files.keyFile, err)
		}
		server.TLSConfig = &tls.Config{
			MinVersion:     tls.VersionTLS12,
			GetCertificate: watcher.GetCertificate,
		}
		go func() {
			if err := watcher.Start(ctx); err != nil {
				failed <- fmt.Errorf("watching TLS certificate: %w", err)
			}
		}()
		// The certificate comes from GetCertificate, so no files are named here.
		go func() { failed <- server.ServeTLS(ln, "", "") }()
	} else {
		go func() { failed <- server.Serve(ln) }()
	}

	select {
	case err := <-failed:
		_ = server.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serving %s on %s: %w", files.scheme(), ln.Addr(), err)
	case <-ctx.Done():
	}

	// The parent context is already cancelled, so the drain needs its own.
	shutdownCtx, stop := context.WithTimeout(context.Background(), shutdownTimeout)
	defer stop()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down: %w", err)
	}
	return nil
}
