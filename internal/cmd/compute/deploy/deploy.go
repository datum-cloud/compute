package deploy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	"go.datum.net/compute/internal/cmd/compute/watch"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type options struct {
	image                string
	instanceType         string
	locations            []string
	cities               []string
	allMatchingLocations bool
	min                  int32
	port                 int32
	file                 string
	yes                  bool
}

func Command() *cobra.Command {
	opts := &options{}

	cmd := &cobra.Command{
		Use:   "deploy [workload-name]",
		Short: "Deploy or update a workload",
		Long: `Deploy a container image as a workload across one or more locations.

If no arguments are given, an interactive prompt guides you through the deployment.
Use -f to apply a workload manifest file instead of flags.`,
		Args: cobra.MaximumNArgs(1),
		Example: `  # Deploy with flags
  datumctl compute deploy api --image=ghcr.io/acme/api:1.4.2 --location=us-east-1,eu-west-1 --min=2 --port=8080

  # Interactive mode
  datumctl compute deploy

  # Manifest-driven
  datumctl compute deploy -f workload.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(cmd, args, opts)
		},
		ValidArgsFunction: util.CompleteWorkloadNamesAndFlags,
	}

	cmd.Flags().StringVar(&opts.image, "image", "", "Container image to deploy (e.g. ghcr.io/acme/api:1.4.2)")
	cmd.Flags().StringVar(&opts.instanceType, "instance-type", "datumcloud/d1-standard-2", "Instance type (e.g. datumcloud/d1-standard-2)")
	cmd.Flags().StringSliceVar(&opts.locations, "location", nil, "One or more locations to deploy to (e.g. us-east-1,eu-west-1)")
	cmd.Flags().StringSliceVar(&opts.cities, "city", nil, "Resolve a city code to an available location (e.g. IAD)")
	cmd.Flags().BoolVar(&opts.allMatchingLocations, "all-matching-locations", false, "Deploy to every location matching --city")
	cmd.Flags().Int32Var(&opts.min, "min", 1, "Minimum number of instances per location")
	cmd.Flags().Int32Var(&opts.port, "port", 0, "Port to expose on the workload (optional)")
	cmd.Flags().StringVarP(&opts.file, "file", "f", "", "Path to a workload manifest file")
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Skip confirmation prompts")
	_ = cmd.RegisterFlagCompletionFunc("location", util.CompleteLocations)
	_ = cmd.RegisterFlagCompletionFunc("city", util.CompleteCityCodes)

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
	if len(opts.locations) == 0 && len(opts.cities) == 0 {
		return fmt.Errorf("--location is required (e.g. --location=us-east-1,eu-west-1); use --city to resolve a city code")
	}
	if len(opts.locations) > 0 && len(opts.cities) > 0 {
		return fmt.Errorf("--location and --city are mutually exclusive")
	}
	instanceType := opts.instanceType
	if instanceType == "" {
		instanceType = "datumcloud/d1-standard-2"
	}

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	ctx := context.Background()
	out := cmd.OutOrStdout()
	locations := opts.locations
	if len(opts.cities) > 0 {
		locations, err = resolveCities(ctx, c, opts.cities, opts.allMatchingLocations)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "Resolved --city to locations: %s\n", strings.Join(locations, ", "))
	}

	if err := ensureNetwork(ctx, cmd, c, "default", project, opts); err != nil {
		return err
	}

	fmt.Fprintf(out, "Resolving workload %q in project %s...\n", workloadName, project)

	var workload computev1alpha.Workload
	creating := false
	if err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: workloadName}, &workload); err != nil {
		if k8serrors.IsNotFound(err) {
			creating = true
			workload = computev1alpha.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: util.ResourceNamespace,
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

	locationRefs := make([]locationsv1alpha1.LocationReference, 0, len(locations))
	for _, name := range locations {
		locationRefs = append(locationRefs, locationsv1alpha1.LocationReference{Name: name})
	}
	// All locations go into one "default" placement.
	placement := computev1alpha.WorkloadPlacement{
		Name:      "default",
		Locations: locationRefs,
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

	fmt.Fprintf(out, "  Placement \"default\": locations=[%s], min=%d\n",
		strings.Join(locations, ", "), opts.min)

	// Prompt unless --yes or non-interactive.
	if !opts.yes && term.IsTerminal(int(os.Stdin.Fd())) {
		_, _ = fmt.Fprint(out, "Apply? (Y/n): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "n" || line == "N" {
			_, _ = fmt.Fprintln(out, "Aborted.")
			return nil
		}
	}

	if creating {
		workload.Namespace = util.ResourceNamespace
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

	// Save workload.yaml.
	if err := saveWorkloadYAML(workloadName, &workload); err != nil {
		fmt.Fprintf(out, "  warning: could not save workload.yaml: %v\n", err)
	} else {
		_, _ = fmt.Fprintln(out, "Saved workload.yaml")
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

	workload.Namespace = util.ResourceNamespace

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	ctx := context.Background()
	out := cmd.OutOrStdout()

	for _, iface := range workload.Spec.Template.Spec.NetworkInterfaces {
		if err := ensureNetwork(ctx, cmd, c, iface.Network.Name, project, opts); err != nil {
			return err
		}
	}

	var existing computev1alpha.Workload
	creating := false
	if err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: workload.Name}, &existing); err != nil {
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
			_, _ = fmt.Fprintln(out, l)
		}
		if len(diffLines) == 0 {
			_, _ = fmt.Fprintln(out, "No changes detected.")
		}
	}

	// Prompt unless --yes or non-interactive.
	if !opts.yes && term.IsTerminal(int(os.Stdin.Fd())) {
		_, _ = fmt.Fprint(out, "Apply? (Y/n): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return fmt.Errorf("reading confirmation: %w", err)
		}
		line = strings.TrimSpace(line)
		if line == "n" || line == "N" {
			_, _ = fmt.Fprintln(out, "Aborted.")
			return nil
		}
	}

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

	fmt.Fprintf(out, "Waiting for rollout. Ctrl-C to detach (rollout continues in background).\n\n")

	watchCtx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer cancel()
	return watch.Rollout(watchCtx, c, out, project, workload.UID)
}

// saveWorkloadYAML marshals the workload and writes it to workload.yaml in the
// current directory.
func saveWorkloadYAML(_ string, workload *computev1alpha.Workload) error {
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

// ensureNetwork checks if the named network exists and, if not, offers to create it.
// It creates a minimal auto-IPAM IPv4 network on behalf of the user.
func ensureNetwork(ctx context.Context, cmd *cobra.Command, c client.Client, networkName, project string, opts *options) error {
	var network networkingv1alpha.Network
	err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: networkName}, &network)
	if err == nil {
		return nil
	}
	if !k8serrors.IsNotFound(err) {
		return fmt.Errorf("checking network %q: %w", networkName, err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "  Network %q does not exist in project %s.\n", networkName, project)

	if !opts.yes && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(out, "  Create it now? (Y/n): ")
		line, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
		if readErr != nil {
			return fmt.Errorf("reading confirmation: %w", readErr)
		}
		line = strings.TrimSpace(line)
		if line == "n" || line == "N" {
			return fmt.Errorf("network %q is required — create it with: datumctl apply -f network.yaml --project %s", networkName, project)
		}
	} else if !opts.yes {
		return fmt.Errorf("network %q not found in project %s — use --yes to auto-create or create it first", networkName, project)
	}

	ipv4Mode := networkingv1alpha.NetworkIPAMModeAuto
	newNetwork := networkingv1alpha.Network{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: util.ResourceNamespace,
			Name:      networkName,
		},
		Spec: networkingv1alpha.NetworkSpec{
			IPAM: networkingv1alpha.NetworkIPAM{
				Mode: ipv4Mode,
			},
		},
	}
	if err := c.Create(ctx, &newNetwork); err != nil {
		return fmt.Errorf("creating network %q: %w", networkName, err)
	}
	fmt.Fprintf(out, "  network/%s created\n", networkName)
	return nil
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
			locationNames := make([]string, 0, len(np.Locations))
			for _, ref := range np.Locations {
				locationNames = append(locationNames, ref.Name)
			}
			lines = append(lines, fmt.Sprintf("  + new placement %q: locations=[%s]",
				name, strings.Join(locationNames, ", ")))
		}
	}
	for name := range oldPlacements {
		if _, ok := newPlacements[name]; !ok {
			lines = append(lines, fmt.Sprintf("  - removed placement %q", name))
		}
	}

	return lines
}

func resolveCities(ctx context.Context, c client.Client, cities []string, all bool) ([]string, error) {
	var list locationsv1alpha1.LocationList
	if err := c.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("listing project locations: %w", err)
	}
	var resolved []string
	for _, city := range cities {
		var matches []string
		for _, location := range list.Items {
			ready := apimeta.FindStatusCondition(location.Status.Conditions, locationsv1alpha1.LocationConditionReady)
			if location.Spec.Topology[locationsv1alpha1.TopologyCityCodeKey] != city || ready == nil || ready.Status != metav1.ConditionTrue {
				continue
			}
			matches = append(matches, location.Name)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("city %q has no Ready locations; run 'datumctl get locations' to see available locations", city)
		}
		if len(matches) > 1 && !all {
			return nil, fmt.Errorf("city %q matches multiple Ready locations (%s); use --location to choose one or --all-matching-locations", city, strings.Join(matches, ", "))
		}
		resolved = append(resolved, matches...)
	}
	return resolved, nil
}
