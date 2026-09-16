package util

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	"go.datum.net/datumctl/plugin"
	"go.miloapis.com/service-catalog/pkg/activation"
	"k8s.io/client-go/rest"
)

// gateSkipAnnotation marks a command (and its subtree) as exempt from the
// service-activation preflight. The access verbs carry it so a user can inspect
// or request access without first being gated on that same access.
const gateSkipAnnotation = "compute.datumapis.com/skip-activation-gate"

// ComputeServiceName is the canonical catalog name of the compute service. The
// activation SDK builds its user-facing commands from this name, for example
// `datumctl services enable compute.datumapis.com`.
const ComputeServiceName = "compute.datumapis.com"

// ResolveComputeService looks up the compute Service in the platform-wide
// catalog.
//
// The SDK reads the enablement mode and description from the live Service
// instead of a hand-written copy. Without the mode, the SDK treats compute as
// self-service. The gate would then submit access requests without asking,
// including from CI, even though compute requires provider approval.
func ResolveComputeService(ctx context.Context) (activation.ServiceInfo, error) {
	cfg, err := restConfig(func(apiHost string) string { return "https://" + apiHost })
	if err != nil {
		return activation.ServiceInfo{}, err
	}
	cc, err := activation.NewCatalogRESTClient(cfg)
	if err != nil {
		return activation.ServiceInfo{}, err
	}
	services, err := cc.ListServices(ctx)
	if err != nil {
		return activation.ServiceInfo{}, fmt.Errorf("looking up the compute service: %w", err)
	}
	return activation.FindService(services, ComputeServiceName)
}

// NewEntitlementClient builds a service-activation client targeting the
// project's virtual control plane, where ServiceEntitlements live.
func NewEntitlementClient(project string) (activation.EntitlementClient, error) {
	cfg, err := restConfig(func(apiHost string) string { return ProjectControlPlaneURL(apiHost, project) })
	if err != nil {
		return nil, err
	}
	return activation.NewRESTClient(cfg)
}

// restConfig builds a REST config from the datumctl-injected API host and a
// fresh credentials-helper token. This is the plugin's half of the SDK's auth
// seam.
func restConfig(hostURL func(apiHost string) string) (*rest.Config, error) {
	ctx := plugin.Context()
	if ctx.APIHost == "" {
		return nil, fmt.Errorf("DATUM_API_HOST is not set; is this plugin running via datumctl?")
	}
	token, err := plugin.Token()
	if err != nil {
		return nil, fmt.Errorf("getting credentials: %w", err)
	}
	return &rest.Config{
		Host:        hostURL(ctx.APIHost),
		BearerToken: token,
	}, nil
}

// MarkGateExempt tags a command so the activation preflight skips it and its
// subcommands.
func MarkGateExempt(cmd *cobra.Command) {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[gateSkipAnnotation] = "true"
}

// RunActivationGate runs the compute activation preflight for the given command.
// An empty project is a no-op; the invoked command surfaces its own clearer
// error. Returns the SDK's typed error (carrying an exit code) or nil to proceed.
func RunActivationGate(cmd *cobra.Command) error {
	project := ProjectFromCmd(cmd)
	if project == "" {
		return nil
	}
	service, err := ResolveComputeService(cmd.Context())
	if err != nil {
		return err
	}
	ec, err := NewEntitlementClient(project)
	if err != nil {
		return err
	}
	gate := activation.Gate{
		Service: service,
		Client:  ec,
		IO:      ActivationIO(cmd),
		Project: project,
	}
	return gate.Run(cmd.Context())
}

// GateExempt reports whether the activation preflight should skip this command:
// the help and shell-completion machinery, and any command tagged gate-exempt
// (the access verbs).
func GateExempt(cmd *cobra.Command) bool {
	switch cmd.Name() {
	case "help", cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd:
		return true
	}
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "completion" {
			return true
		}
		if c.Annotations[gateSkipAnnotation] == "true" {
			return true
		}
	}
	return false
}

// ActivationIO adapts a command's streams to the SDK's IOStreams.
func ActivationIO(cmd *cobra.Command) activation.IOStreams {
	return activation.IOStreams{
		In:  cmd.InOrStdin(),
		Out: cmd.OutOrStdout(),
		Err: cmd.ErrOrStderr(),
	}
}
