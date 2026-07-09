package access

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"go.datum.net/datumctl/serviceactivation"

	"go.datum.net/compute/internal/cmd/compute/util"
)

// Command returns the `access` command group: a gate-exempt way to inspect or
// request Compute service access for the current project.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "access",
		Short: "Show or request Compute service access for the current project",
		Long: "Show the current Compute service-access state for the project, or " +
			"request access with the request subcommand.",
		RunE: runStatus,
	}
	util.MarkGateExempt(cmd)

	cmd.Flags().StringP("output", "o", "table", "Output format: table, json, yaml")
	_ = cmd.RegisterFlagCompletionFunc("output", util.CompleteOutputFormats("table", "json", "yaml"))

	cmd.AddCommand(requestCommand())
	return cmd
}

// runStatus prints the derived access state. It always exits 0 — state is data,
// not an error — so it returns nil for every activation state and only surfaces
// genuine transport failures.
func runStatus(cmd *cobra.Command, _ []string) error {
	project := util.ProjectFromCmd(cmd)
	if project == "" {
		return fmt.Errorf("no project set — pass --project or run 'datumctl config set project <name>'")
	}

	ec, err := util.NewEntitlementClient(project)
	if err != nil {
		return err
	}

	cfg := util.ActivationConfig()
	state, entitlement, err := serviceactivation.Observe(cmd.Context(), ec, cfg)
	if err != nil {
		return fmt.Errorf("checking compute access: %w", err)
	}

	outputFlag, _ := cmd.Flags().GetString("output")
	switch util.OutputFormat(outputFlag) {
	case util.OutputJSON:
		return util.PrintJSON(cmd.OutOrStdout(), serviceactivation.NewStatusReport(cfg, project, state, entitlement))
	case util.OutputYAML:
		return util.PrintYAML(cmd.OutOrStdout(), serviceactivation.NewStatusReport(cfg, project, state, entitlement))
	default:
		serviceactivation.RenderStatus(cmd.OutOrStdout(), cfg, project, state, entitlement)
		return nil
	}
}

// requestCommand returns `access request`: the explicit, non-interactive-safe
// request verb. Invoking it is consent, so it never prompts (except a --renew
// confirmation on a TTY).
func requestCommand() *cobra.Command {
	var (
		message string
		renew   bool
		wait    bool
		timeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "request",
		Short: "Request Compute service access for the current project",
		Long: "Submit a request to enable the Compute service for the current " +
			"project. Approval may be a manual step by the service provider.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			project := util.ProjectFromCmd(cmd)
			if project == "" {
				return fmt.Errorf("no project set — pass --project or run 'datumctl config set project <name>'")
			}
			ec, err := util.NewEntitlementClient(project)
			if err != nil {
				return err
			}
			requester := serviceactivation.Requester{
				Config:  util.ActivationConfig(),
				Client:  ec,
				IO:      util.ActivationIO(cmd),
				Project: project,
			}
			return requester.Request(cmd.Context(), serviceactivation.RequestOptions{
				Message: message,
				Renew:   renew,
				Wait:    wait,
				Timeout: timeout,
			})
		},
	}
	util.MarkGateExempt(cmd)

	cmd.Flags().StringVar(&message, "message", "", "Justification sent to the service provider with the request")
	cmd.Flags().BoolVar(&renew, "renew", false, "Delete a denied or revoked request and submit a new one")
	cmd.Flags().BoolVar(&wait, "wait", false, "Wait until access becomes active (approval is a manual provider step)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Minute, "Maximum time to wait when --wait is set")

	return cmd
}
