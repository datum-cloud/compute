// SPDX-License-Identifier: AGPL-3.0-only

// Command compute-mcp serves compute's read-only diagnostic tools over MCP,
// alongside the knowledge and skills an assistant reads before calling them:
//
//	POST /mcp                   Streamable HTTP MCP, stateless
//	GET  /llms-full.txt         Knowledge: the compute resource model
//	GET  /runbooks/<name>.md    Skills: triage procedures
//	GET  /healthz               liveness
//
// One process, because compute's capability document names all of those URLs
// and a second deployment to satisfy the static ones would be a second thing to
// keep in step. Only /mcp takes a credential; see docs.go for why the documents
// do not.
//
// Every read runs as the caller. The server holds no credential of its own for
// the project control plane: it takes the bearer token off the incoming
// request, builds a client with it, and reads through that. So a tool call can
// never see more than the person who asked could see themselves, the platform's
// RBAC stays the single enforcement point, and the server needs no
// impersonation privilege — there is nothing here to escalate with.
//
// The project a request reads is taken from a header, never from a tool
// argument. That is deliberate: tool arguments are chosen by the model, and a
// model that could name its own project would be one prompt-injection away
// from reading another tenant's workloads. The header is set by the caller
// (the assistant/gateway), which has already authenticated the user.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/agent"
)

const (
	// projectHeader names the project whose control plane a request reads. Set
	// by the caller, not by the model.
	projectHeader = "X-Datum-Project"

	// serverName and serverVersion identify this server to MCP clients.
	serverName    = "datum-compute-mcp"
	serverVersion = "0.1.0"

	// readHeaderTimeout bounds how long a slow client may hold a connection
	// before sending headers.
	readHeaderTimeout = 10 * time.Second

	// resourceNamespace is the namespace compute's objects live in inside a
	// project's control plane. The project routes to the control plane; within
	// it everything is in "default". Mirrors util.ResourceNamespace, which the
	// CLI uses for the same objects; not imported because that package pulls in
	// the whole datumctl plugin runtime.
	resourceNamespace = "default"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(computev1alpha.AddToScheme(scheme))
}

func main() {
	var addr string

	flag.StringVar(&addr, "addr", envOr("COMPUTE_MCP_ADDR", ":8080"),
		"address to serve MCP on")

	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// GetConfig resolves in-cluster config, the --kubeconfig flag that
	// controller-runtime registers, KUBECONFIG, then ~/.kube/config. Only the
	// endpoint and CA are used; see clientForToken.
	baseConfig, err := ctrl.GetConfig()
	if err != nil {
		setupLog.Error(err, "unable to load control plane configuration")
		os.Exit(1)
	}

	if err := run(addr, baseConfig); err != nil {
		setupLog.Error(err, "server failed")
		os.Exit(1)
	}
}

func run(addr string, baseConfig *rest.Config) error {
	handler := mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server {
			// A server per request, bound to that caller's identity and
			// project. Nothing is shared between callers.
			s := mcp.NewServer(&mcp.Implementation{
				Name:    serverName,
				Version: serverVersion,
			}, nil)
			agent.RegisterTools(s, depsFromRequest(r, baseConfig))
			return s
		},
		// Stateless: no session state is needed for read-only tools, and it
		// keeps the server robust against client crashes.
		&mcp.StreamableHTTPOptions{Stateless: true},
	)

	// The knowledge and skills the capability document points the assistant at.
	// They are served from this same process so that one deployment satisfies
	// every URL compute publishes.
	docs, err := newKnowledgeHandler()
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", handler)
	mux.Handle(knowledgePath, docs)
	mux.Handle(runbookPrefix, docs)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, "ok")
	})

	server := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	setupLog.Info("listening", "addr", addr, "mcp", "/mcp", "docs", docs.paths())
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving on %s: %w", addr, err)
	}
	return nil
}

// depsFromRequest returns a DepsFor that resolves the caller's identity and
// project from r.
//
// Resolution is deferred to call time rather than done here so that a request
// carrying no credentials fails as a tool error the model can report, rather
// than as a nil client panic deep in a handler.
func depsFromRequest(r *http.Request, baseConfig *rest.Config) agent.DepsFor {
	token := bearerToken(r)
	project := strings.TrimSpace(r.Header.Get(projectHeader))

	return func(context.Context) (agent.ToolDeps, error) {
		if token == "" {
			return agent.ToolDeps{}, fmt.Errorf(
				"no bearer token on the request: compute reads as the calling user, so the caller must forward their credentials")
		}
		if project == "" {
			return agent.ToolDeps{}, fmt.Errorf("no project on the request: set the %s header", projectHeader)
		}

		c, err := clientForToken(baseConfig, token, project)
		if err != nil {
			return agent.ToolDeps{}, err
		}
		return agent.ToolDeps{Reader: agent.NewClientReader(c), Namespace: resourceNamespace}, nil
	}
}

// projectControlPlanePath returns the API path for a project's control plane.
//
// A project is addressed by rewriting the host path, not by a namespace named
// after it: this mirrors the rewrite the Milo multicluster provider performs,
// and matches internal/quota/client.go, internal/referenceddata/project_reader.go
// and internal/cmd/compute/util/client.go.
func projectControlPlanePath(project string) string {
	return fmt.Sprintf("/apis/resourcemanager.miloapis.com/v1alpha1/projects/%s/control-plane", project)
}

// clientConfig derives the REST config one request reads through: the caller's
// token, pointed at the caller's project control plane.
//
// The base config supplies the endpoint and CA only; every credential field it
// might carry is cleared first, so the server's own identity (a mounted service
// account token, a kubeconfig's client certificate) can never leak into a
// caller's read.
func clientConfig(baseConfig *rest.Config, token, project string) (*rest.Config, error) {
	// The project arrives in a header and is interpolated into a URL path, so
	// it is validated before it can reshape that path into another API route.
	if errs := validation.IsDNS1123Subdomain(project); len(errs) > 0 {
		return nil, fmt.Errorf("invalid project %q on the %s header: %s", project, projectHeader, strings.Join(errs, "; "))
	}

	cfg := rest.AnonymousClientConfig(rest.CopyConfig(baseConfig))
	cfg.BearerToken = token
	cfg.BearerTokenFile = ""

	host, err := url.Parse(cfg.Host)
	if err != nil {
		return nil, fmt.Errorf("parsing control plane host: %w", err)
	}
	host.Path = projectControlPlanePath(project)
	cfg.Host = host.String()

	return cfg, nil
}

// clientForToken builds a client that reads project's control plane as the
// bearer of token.
func clientForToken(baseConfig *rest.Config, token, project string) (client.Client, error) {
	cfg, err := clientConfig(baseConfig, token, project)
	if err != nil {
		return nil, err
	}

	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("building client for caller: %w", err)
	}
	return c, nil
}

// bearerToken extracts a bearer token from the Authorization header.
func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	auth := r.Header.Get("Authorization")
	if len(auth) < len(prefix) || !strings.EqualFold(auth[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
