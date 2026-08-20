package util

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.datum.net/datumctl/plugin"
	"go.datum.net/datumctl/serviceactivation"
	"k8s.io/client-go/rest"
)

// gateSkipAnnotation marks a command (and its subtree) as exempt from the
// service-activation preflight. The access verbs carry it so a user can inspect
// or request access without first being gated on that same access.
const gateSkipAnnotation = "compute.datumapis.com/skip-activation-gate"

// ActivationConfig is the service-activation configuration for the compute
// service. All user-facing copy is templated on these values, so the SDK prints
// only plugin-local verbs (datumctl compute access ...).
func ActivationConfig() serviceactivation.Config {
	return serviceactivation.Config{
		ObjectName:    "compute",
		CanonicalName: "compute.datumapis.com",
		DisplayName:   "Compute",
		AccessCommand: "datumctl compute access",
	}
}

// NewEntitlementClient builds a service-activation client targeting the
// project's virtual control plane, using the datumctl-injected host and a fresh
// credentials-helper token. This is the plugin's half of the SDK's auth seam.
func NewEntitlementClient(project string) (serviceactivation.EntitlementClient, error) {
	ctx := plugin.Context()
	if ctx.APIHost == "" {
		return nil, fmt.Errorf("DATUM_API_HOST is not set; is this plugin running via datumctl?")
	}
	token, err := plugin.Token()
	if err != nil {
		return nil, fmt.Errorf("getting credentials: %w", err)
	}
	cfg := &rest.Config{
		Host:        ProjectControlPlaneURL(ctx.APIHost, project),
		BearerToken: token,
	}
	return serviceactivation.NewRESTClient(cfg)
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
	ec, err := NewEntitlementClient(project)
	if err != nil {
		return err
	}
	gate := serviceactivation.Gate{
		Config:  ActivationConfig(),
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
func ActivationIO(cmd *cobra.Command) serviceactivation.IOStreams {
	return serviceactivation.IOStreams{
		In:  cmd.InOrStdin(),
		Out: cmd.OutOrStdout(),
		Err: cmd.ErrOrStderr(),
	}
}
