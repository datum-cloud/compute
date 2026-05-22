package deploy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"golang.org/x/term"
	sigsyaml "sigs.k8s.io/yaml"

	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/revision"
	"go.datum.net/compute/internal/cmd/compute/util"
	"go.datum.net/compute/internal/cmd/compute/watch"
)

type options struct {
	image        string
	instanceType string
	cities       []string
	min          int32
	port         int32
	file         string
	yes          bool
}

func Command() *cobra.Command {
	opts := &options{}

	cmd := &cobra.Command{
		Use:   "deploy [workload-name]",
		Short: "Deploy or update a workload",
		Long: `Deploy a container image as a workload across one or more cities.

If no arguments are given, an interactive prompt guides you through the deployment.
Use -f to apply a workload manifest file instead of flags.`,
		Args:    cobra.MaximumNArgs(1),
		Example: `  # Deploy with flags
  datumctl compute deploy api --image=ghcr.io/acme/api:1.4.2 --city=DFW,IAD --min=2 --port=8080

  # Interactive mode
  datumctl compute deploy

  # Manifest-driven
  datumctl compute deploy -f workload.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(cmd, args, opts)
		},
	}

	cmd.Flags().StringVar(&opts.image, "image", "", "Container image to deploy (e.g. ghcr.io/acme/api:1.4.2)")
	cmd.Flags().StringVar(&opts.instanceType, "instance-type", "d1-standard-2", "Instance type (e.g. d1-standard-2)")
	cmd.Flags().StringSliceVar(&opts.cities, "city", nil, "One or more city codes to deploy to (e.g. DFW,IAD)")
	cmd.Flags().Int32Var(&opts.min, "min", 1, "Minimum number of instances per city")
	cmd.Flags().Int32Var(&opts.port, "port", 0, "Port to expose on the workload (optional)")
	cmd.Flags().StringVarP(&opts.file, "file", "f", "", "Path to a workload manifest file")
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Skip confirmation prompts")

	return cmd
}

func runDeploy(cmd *cobra.Command, args []string, opts *options) error {
	// Determine path.
	if opts.file != "" {
		return deployFromFile(cmd, opts)
	}

	if len(args) > 0 && opts.image != "" {
		return deployFromFlags(cmd, args[0], opts)
	}

	return fmt.Errorf("workload name and --image are required, or use -f to specify a manifest file")
}

// deployFromFlags implements Path A: deploy a workload using CLI flags.
func deployFromFlags(cmd *cobra.Command, workloadName string, opts *options) error {
	project := util.ProjectFromCmd(cmd)
	if project == "" {
		return fmt.Errorf("no project set — pass --project or run 'datumctl config set project <name>'")
	}
	if opts.image == "" {
		return fmt.Errorf("--image is required")
	}
	if len(opts.cities) == 0 {
		return fmt.Errorf("--city is required (e.g. --city=DFW,IAD)")
	}
	instanceType := opts.instanceType
	if instanceType == "" {
		instanceType = "d1-standard-2"
	}

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	ctx := context.Background()
	out := cmd.OutOrStdout()

	fmt.Fprintf(out, "Resolving workload %q in project %s...\n", workloadName, project)

	var workload computev1alpha.Workload
	creating := false
	if err := c.Get(ctx, types.NamespacedName{Namespace: project, Name: workloadName}, &workload); err != nil {
		if k8serrors.IsNotFound(err) {
			creating = true
			workload = computev1alpha.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: project,
					Name:      workloadName,
				},
			}
		} else {
			return fmt.Errorf("getting workload: %w", err)
		}
	}

	// Build spec.
	tcp := corev1.ProtocolTCP
	container := computev1alpha.SandboxContainer{
		Name:  "app",
		Image: opts.image,
	}
	if opts.port > 0 {
		container.Ports = []computev1alpha.NamedPort{
			{Name: "http", Port: opts.port, Protocol: &tcp},
		}
	}

	// All cities go into one "default" placement.
	placement := computev1alpha.WorkloadPlacement{
		Name:      "default",
		CityCodes: opts.cities,
		ScaleSettings: computev1alpha.HorizontalScaleSettings{
			MinReplicas:              opts.min,
			InstanceManagementPolicy: computev1alpha.OrderedReadyInstanceManagementPolicyType,
		},
	}

	workload.Spec = computev1alpha.WorkloadSpec{
		Template: computev1alpha.InstanceTemplateSpec{
			Spec: computev1alpha.InstanceSpec{
				Runtime: computev1alpha.InstanceRuntimeSpec{
					Resources: computev1alpha.InstanceRuntimeResources{
						InstanceType: instanceType,
					},
					Sandbox: &computev1alpha.SandboxRuntime{
						Containers: []computev1alpha.SandboxContainer{container},
					},
				},
				NetworkInterfaces: []computev1alpha.InstanceNetworkInterface{
					{
						// TODO: "default" network name is a convention; confirm with platform team.
						Network: networkingv1alpha.NetworkRef{Name: "default"},
					},
				},
			},
		},
		Placements: []computev1alpha.WorkloadPlacement{placement},
	}

	fmt.Fprintf(out, "  Placement \"default\": cities=[%s], min=%d\n",
		strings.Join(opts.cities, ", "), opts.min)

	// Prompt unless --yes or non-interactive.
	if !opts.yes && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(out, "Apply? (Y/n): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "n" || line == "N" {
			fmt.Fprintln(out, "Aborted.")
			return nil
		}
	}

	// Compute diff description before applying.
	var changes string
	if creating {
		changes = "initial deploy"
	} else {
		changes = computeDiff(workload.Spec, opts.image, opts.cities, opts.min)
	}

	if creating {
		workload.Namespace = project
		if err := c.Create(ctx, &workload); err != nil {
			return fmt.Errorf("creating workload: %w", err)
		}
		fmt.Fprintf(out, "  workload/%s created\n", workloadName)
	} else {
		if err := c.Update(ctx, &workload); err != nil {
			return fmt.Errorf("updating workload: %w", err)
		}
		fmt.Fprintf(out, "  workload/%s updated\n", workloadName)
	}

	// Write revision entry.
	rev := revision.CurrentRevision(ctx, c, project, workloadName) + 1
	specJSON, _ := json.Marshal(workload.Spec)
	entry := revision.Entry{
		Rev:       rev,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Image:     opts.image,
		Changes:   changes,
		SpecJSON:  string(specJSON),
	}
	if err := revision.WriteEntry(ctx, c, project, workloadName, entry); err != nil {
		// Non-fatal — log but continue.
		fmt.Fprintf(out, "  warning: could not write revision history: %v\n", err)
	}

	// Save workload.yaml.
	if err := saveWorkloadYAML(workloadName, &workload); err != nil {
		fmt.Fprintf(out, "  warning: could not save workload.yaml: %v\n", err)
	} else {
		fmt.Fprintln(out, "Saved workload.yaml")
	}

	fmt.Fprintf(out, "Waiting for rollout. Ctrl-C to detach (rollout continues in background).\n\n")

	watchCtx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer cancel()
	return watch.Rollout(watchCtx, c, out, project, workload.UID)
}

// deployFromFile implements Path C: deploy from a manifest file.
func deployFromFile(cmd *cobra.Command, opts *options) error {
	project := util.ProjectFromCmd(cmd)
	if project == "" {
		return fmt.Errorf("no project set — pass --project or run 'datumctl config set project <name>'")
	}

	data, err := os.ReadFile(opts.file)
	if err != nil {
		return fmt.Errorf("reading manifest: %w", err)
	}

	var workload computev1alpha.Workload
	decoder := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
	if err := decoder.Decode(&workload); err != nil {
		return fmt.Errorf("decoding manifest: %w", err)
	}

	workload.Namespace = project

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	ctx := context.Background()
	out := cmd.OutOrStdout()

	var existing computev1alpha.Workload
	creating := false
	if err := c.Get(ctx, types.NamespacedName{Namespace: project, Name: workload.Name}, &existing); err != nil {
		if k8serrors.IsNotFound(err) {
			creating = true
		} else {
			return fmt.Errorf("getting workload: %w", err)
		}
	}

	var diffLines []string
	if !creating {
		diffLines = manifestDiff(existing, workload)
		for _, l := range diffLines {
			fmt.Fprintln(out, l)
		}
		if len(diffLines) == 0 {
			fmt.Fprintln(out, "No changes detected.")
		}
	}

	// Prompt unless --yes or non-interactive.
	if !opts.yes && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(out, "Apply? (Y/n): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "n" || line == "N" {
			fmt.Fprintln(out, "Aborted.")
			return nil
		}
	}

	// Determine changes summary.
	var changes string
	if creating {
		changes = "initial deploy"
	} else if len(diffLines) > 0 {
		changes = strings.Join(diffLines, "; ")
	} else {
		changes = "manifest apply"
	}

	image := imageFromWorkload(workload)

	if creating {
		if err := c.Create(ctx, &workload); err != nil {
			return fmt.Errorf("creating workload: %w", err)
		}
		fmt.Fprintf(out, "  workload/%s created\n", workload.Name)
	} else {
		workload.ResourceVersion = existing.ResourceVersion
		if err := c.Update(ctx, &workload); err != nil {
			return fmt.Errorf("updating workload: %w", err)
		}
		fmt.Fprintf(out, "  workload/%s updated\n", workload.Name)
	}

	rev := revision.CurrentRevision(ctx, c, project, workload.Name) + 1
	specJSON, _ := json.Marshal(workload.Spec)
	entry := revision.Entry{
		Rev:       rev,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Image:     image,
		Changes:   changes,
		SpecJSON:  string(specJSON),
	}
	if err := revision.WriteEntry(ctx, c, project, workload.Name, entry); err != nil {
		fmt.Fprintf(out, "  warning: could not write revision history: %v\n", err)
	}

	fmt.Fprintf(out, "Waiting for rollout. Ctrl-C to detach (rollout continues in background).\n\n")

	watchCtx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer cancel()
	return watch.Rollout(watchCtx, c, out, project, workload.UID)
}

// saveWorkloadYAML marshals the workload and writes it to workload.yaml in the
// current directory.
func saveWorkloadYAML(workloadName string, workload *computev1alpha.Workload) error {
	workload.TypeMeta = metav1.TypeMeta{
		APIVersion: "compute.datumapis.com/v1alpha",
		Kind:       "Workload",
	}

	data, err := sigsyaml.Marshal(workload)
	if err != nil {
		return fmt.Errorf("marshalling workload: %w", err)
	}

	header := "# Managed by datumctl compute deploy. Commit this file to manage your workload declaratively.\n" +
		"# Apply changes with: datumctl compute deploy -f workload.yaml\n"

	return os.WriteFile("workload.yaml", append([]byte(header), data...), 0o644)
}

// imageFromWorkload returns the first container image found in a workload, or empty string.
func imageFromWorkload(w computev1alpha.Workload) string {
	sb := w.Spec.Template.Spec.Runtime.Sandbox
	if sb != nil && len(sb.Containers) > 0 {
		return sb.Containers[0].Image
	}
	return ""
}

// computeDiff produces a human-readable one-line diff description for flag-driven updates.
func computeDiff(existing computev1alpha.WorkloadSpec, newImage string, cities []string, min int32) string {
	var parts []string

	oldImage := ""
	if existing.Template.Spec.Runtime.Sandbox != nil && len(existing.Template.Spec.Runtime.Sandbox.Containers) > 0 {
		oldImage = existing.Template.Spec.Runtime.Sandbox.Containers[0].Image
	}
	if oldImage != newImage {
		parts = append(parts, fmt.Sprintf("image: %s → %s", oldImage, newImage))
	}

	if len(existing.Placements) > 0 {
		oldMin := existing.Placements[0].ScaleSettings.MinReplicas
		if oldMin != min {
			parts = append(parts, fmt.Sprintf("min replicas: %d → %d", oldMin, min))
		}
	}

	if len(parts) == 0 {
		return "no changes"
	}
	return strings.Join(parts, ", ")
}

// manifestDiff computes diff lines between an existing and desired workload.
func manifestDiff(existing, desired computev1alpha.Workload) []string {
	var lines []string

	oldImage := imageFromWorkload(existing)
	newImage := imageFromWorkload(desired)
	if oldImage != newImage {
		lines = append(lines, fmt.Sprintf("  image: %s → %s", oldImage, newImage))
	}

	// Compare placements by name.
	oldPlacements := make(map[string]computev1alpha.WorkloadPlacement)
	for _, p := range existing.Spec.Placements {
		oldPlacements[p.Name] = p
	}
	newPlacements := make(map[string]computev1alpha.WorkloadPlacement)
	for _, p := range desired.Spec.Placements {
		newPlacements[p.Name] = p
	}

	for name, np := range newPlacements {
		if op, ok := oldPlacements[name]; ok {
			if op.ScaleSettings.MinReplicas != np.ScaleSettings.MinReplicas {
				lines = append(lines, fmt.Sprintf("  placement %q min replicas: %d → %d",
					name, op.ScaleSettings.MinReplicas, np.ScaleSettings.MinReplicas))
			}
		} else {
			lines = append(lines, fmt.Sprintf("  + new placement %q: cities=[%s]",
				name, strings.Join(np.CityCodes, ", ")))
		}
	}
	for name := range oldPlacements {
		if _, ok := newPlacements[name]; !ok {
			lines = append(lines, fmt.Sprintf("  - removed placement %q", name))
		}
	}

	return lines
}

