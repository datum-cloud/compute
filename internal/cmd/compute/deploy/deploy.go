package deploy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	sigsyaml "sigs.k8s.io/yaml"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/build"
	"go.datum.net/compute/internal/cmd/compute/url"
	"go.datum.net/compute/internal/cmd/compute/util"
	"go.datum.net/compute/internal/cmd/compute/watch"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
	locationsv1alpha1 "go.miloapis.com/locations/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// httpPortName is the name given to the container port --http-port
	// declares, and the name the URL's backend reference points at.
	httpPortName = "http"

	// planLabelWidth is the label column of the plan summary printed before
	// the Apply prompt, so every line in it starts its value at the same
	// column.
	planLabelWidth = 20
)

// errPortRenamed is the one-release migration for --port. It is an error and
// not an alias on purpose: silently mapping it to --http-port would publish
// every existing workload on the internet at the next plugin upgrade.
var errPortRenamed = errors.New(
	"--port has been replaced by --http-port, which publishes the workload on a public HTTPS URL. " +
		"Use --http-port 8080 to publish, or --no-http to keep it internal")

type options struct {
	image            string
	build            string
	instanceType     string
	locations        []string
	locationSelector string
	cities           []string
	min              int32
	max              int32
	cpuPercent       int32
	memoryPercent    int32
	httpPort         int32
	noHTTP           bool
	port             int32
	file             string
	yes              bool
}

// Command returns the deploy command.
func Command() *cobra.Command {
	cmd, _ := command()
	return cmd
}

// command builds the deploy command and hands back the options it writes into,
// so flag validation can be exercised without a control plane.
func command() (*cobra.Command, *options) {
	opts := &options{}

	cmd := &cobra.Command{
		Use:   "deploy [workload-name]",
		Short: "Deploy or update a workload",
		Long: `Deploy a container image as a workload across one or more locations.

If no arguments are given, an interactive prompt guides you through the deployment.
Use -f to apply a workload manifest file instead of flags.

Use --build to build and push the image before deploying, instead of running
'datumctl compute build' separately. It builds from the given directory (default
Dockerfile discovery, no build-arg/target overrides — use 'datumctl compute build'
directly if you need those) and pushes to --image, which the deployed workload
then pins by digest rather than the tag you gave it. It also analyzes and
auto-fixes common compatibility issues, rewriting the Dockerfile in place when
a fix is applied — same as 'datumctl compute build --fix'.

Use --http-port to declare that the workload is an HTTP service. Declaring one
publishes the workload on a Datum-managed HTTPS URL, printed as the last line
of a successful deploy. Omitting --http-port on an existing workload leaves its
HTTP service as it is; --no-http removes it and stops serving.`,
		Args: cobra.MaximumNArgs(1),
		Example: `  # Deploy with flags
  datumctl compute deploy api --image=ghcr.io/acme/api:1.4.2 --location=us-east-1,eu-west-1 --min=2 --http-port=8080

  # Deploy an internal workload (no URL)
  datumctl compute deploy worker --image=ghcr.io/acme/worker:2.0 --location=us-east-1

  # Stop serving: remove the HTTP service and its URL
  datumctl compute deploy api --image=ghcr.io/acme/api:1.4.2 --location=us-east-1,eu-west-1 --no-http

  # Build and deploy in one step (builds ., pushes to --image, deploys that digest)
  datumctl compute deploy api --build --image=ghcr.io/acme/api:1.4.2 --location=us-east-1,eu-west-1

  # Build from another directory
  datumctl compute deploy api --build=./api --image=ghcr.io/acme/api:1.4.2 --location=us-east-1,eu-west-1

  # Deploy to every location in one or more cities
  datumctl compute deploy api --image=ghcr.io/acme/api:1.4.2 --city=DFW,IAD

  # Select locations by any topology label
  datumctl compute deploy api --image=ghcr.io/acme/api:1.4.2 --location-selector='topology.datum.net/region=us-east-1'

  # Interactive mode
  datumctl compute deploy

  # Manifest-driven
  datumctl compute deploy -f workload.yaml`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDeploy(cmd, args, opts)
		},
		ValidArgsFunction: util.CompleteWorkloadNamesAndFlags,
	}

	cmd.Flags().StringVar(&opts.image, "image", "", "Container image to deploy (e.g. ghcr.io/acme/api:1.4.2); also the push destination when --build is set")
	cmd.Flags().StringVar(&opts.build, "build", "", "Build and push the image from this directory before deploying (default \".\" if given with no value)")
	cmd.Flags().Lookup("build").NoOptDefVal = "."
	cmd.Flags().StringVar(&opts.instanceType, "instance-type", "datumcloud/d1-standard-2", "Instance type (e.g. datumcloud/d1-standard-2)")
	cmd.Flags().StringSliceVar(&opts.locations, "location", nil, "One or more locations to deploy to (e.g. us-east-1,eu-west-1)")
	cmd.Flags().StringVar(&opts.locationSelector, "location-selector", "", "Select every location whose topology matches a label selector (e.g. 'topology.datum.net/city-code=DFW' or 'topology.datum.net/region in (us-east-1,eu-west-1)')")
	cmd.Flags().StringSliceVar(&opts.cities, "city", nil, "Deploy to every location in these cities (e.g. DFW,IAD); shorthand for a --location-selector on topology.datum.net/city-code")
	util.AddScaleFlags(cmd, &opts.min, &opts.max, &opts.cpuPercent, &opts.memoryPercent, 1)
	cmd.Flags().Int32Var(&opts.httpPort, "http-port", 0, "Port the container serves HTTP on; publishes the workload on a Datum-managed HTTPS URL")
	cmd.Flags().BoolVar(&opts.noHTTP, "no-http", false, "Remove the workload's HTTP service, and with it its URL")
	cmd.Flags().StringVarP(&opts.file, "file", "f", "", "Path to a workload manifest file")
	cmd.Flags().BoolVarP(&opts.yes, "yes", "y", false, "Skip confirmation prompts")
	_ = cmd.RegisterFlagCompletionFunc("location", util.CompletePlacementLocations)
	_ = cmd.RegisterFlagCompletionFunc("location-selector", util.CompleteLocationSelector)
	_ = cmd.RegisterFlagCompletionFunc("city", util.CompleteCityCodes)

	// --port stays registered for one release so that using it produces the
	// migration error rather than "unknown flag". It is hidden rather than
	// deprecated: cobra's deprecation only warns and proceeds, and printing a
	// warning above the error that follows says the same thing twice.
	cmd.Flags().Int32Var(&opts.port, "port", 0, "Removed: use --http-port")
	_ = cmd.Flags().MarkHidden("port")

	return cmd, opts
}

// validateFlags rejects flag combinations before the command builds a client
// or creates anything, so an upgrade that trips the --port break costs a
// message and not a workload.
func validateFlags(cmd *cobra.Command, opts *options) error {
	if cmd.Flags().Changed("port") {
		return errPortRenamed
	}

	httpPortSet := cmd.Flags().Changed("http-port")

	if httpPortSet && opts.noHTTP {
		return fmt.Errorf("--http-port and --no-http cannot be combined — pass --http-port to publish the workload, or --no-http to stop serving it")
	}

	if opts.file != "" {
		switch {
		case httpPortSet:
			// TODO: a manifest has no way to declare an HTTP service yet.
			// Resolving that is an API conversation (a field on the workload
			// spec), not CLI sugar layered on top of -f.
			return fmt.Errorf("--http-port cannot be combined with -f: a manifest declares its own ports, and declaring an HTTP service in a manifest is not supported yet")
		case opts.noHTTP:
			return fmt.Errorf("--no-http cannot be combined with -f: remove the URL with 'datumctl compute destroy', or deploy with flags")
		}
	}

	if httpPortSet && (opts.httpPort < 1 || opts.httpPort > 65535) {
		return fmt.Errorf("--http-port must be between 1 and 65535, got %d", opts.httpPort)
	}

	return nil
}

func runDeploy(cmd *cobra.Command, args []string, opts *options) error {
	if err := validateFlags(cmd, opts); err != nil {
		return err
	}

	// Determine path.
	if opts.file != "" {
		if opts.build != "" {
			return fmt.Errorf("--build cannot be combined with -f; a manifest already specifies its own image")
		}
		return deployFromFile(cmd, opts)
	}

	if len(args) > 0 && opts.image != "" {
		if opts.build != "" {
			if kraftfile := build.FindKraftfile(opts.build); kraftfile != "" {
				return fmt.Errorf(
					"found %s: Kraftfile-based builds aren't supported by --build, since they delegate entirely to the "+
						"unikraft CLI. Run the build and deploy steps separately instead:\n"+
						"  datumctl compute build --push --output <ref> .\n"+
						"  datumctl compute deploy --image <ref>", kraftfile)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Building %s and pushing to %s...\n", opts.build, opts.image)
			digest, err := build.Run(cmd.Context(), &build.Options{
				ContextDir: opts.build,
				Dockerfile: "Dockerfile",
				Output:     opts.image,
				Push:       true, // a combined build+deploy step always pushes: there's nothing to confirm
				Fix:        true, // deploying a broken image is worse than auto-fixing and rebuilding
			})
			if err != nil {
				return err
			}
			opts.image = digest
		}
		return deployFromFlags(cmd, args[0], opts)
	}

	return fmt.Errorf("workload name and --image are required, or use -f to specify a manifest file")
}

// deployFromFlags implements Path A: deploy a workload using CLI flags.
// resolveLocationSelector validates the three mutually exclusive ways a deploy
// can say where to run, and returns the selector they resolve to. --location
// names its locations outright and needs no selector, so a nil return with a
// nil error means "the locations were named".
func resolveLocationSelector(opts *options) (*metav1.LabelSelector, error) {
	set := 0
	for _, given := range []bool{len(opts.locations) > 0, len(opts.cities) > 0, opts.locationSelector != ""} {
		if given {
			set++
		}
	}
	switch {
	case set == 0:
		return nil, fmt.Errorf("--location is required (e.g. --location=us-east-1,eu-west-1); or use --city to deploy to every location in a city, or --location-selector to select locations by topology")
	case set > 1:
		return nil, fmt.Errorf("--location, --city, and --location-selector are mutually exclusive")
	}

	if opts.locationSelector != "" {
		parsed, err := metav1.ParseToLabelSelector(opts.locationSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid --location-selector %q: %w", opts.locationSelector, err)
		}
		return parsed, nil
	}
	if len(opts.cities) > 0 {
		return computev1alpha.CityCodeSelector(opts.cities), nil
	}
	return nil, nil
}

// resolveScaleSettings picks the baseline scale settings for this deploy's
// flags to merge onto, and returns the merged, validated result.
//
// The baseline is the one existing placement's settings, under any name, so
// an unrelated flag change doesn't reset previously configured autoscaling.
// A multi-placement workload (only possible via a manifest deploy) has no
// single baseline to preserve; it warns via out before dropping autoscaling
// rather than doing so silently.
func resolveScaleSettings(
	cmd *cobra.Command, out io.Writer, opts *options, existingPlacements []computev1alpha.WorkloadPlacement,
) (computev1alpha.HorizontalScaleSettings, error) {
	current := computev1alpha.HorizontalScaleSettings{MinReplicas: 1}

	switch len(existingPlacements) {
	case 0:
		// Fresh workload; the default above stands.
	case 1:
		current = existingPlacements[0].ScaleSettings
	default:
		for _, p := range existingPlacements {
			if p.ScaleSettings.MaxReplicas != nil {
				fmt.Fprintln(out, planNote(fmt.Sprintf(
					"replacing %d placements with one; placement %q's autoscaling settings will be dropped",
					len(existingPlacements), p.Name)))
				break
			}
		}
	}

	return util.MergeScaleSettings(cmd, current, opts.min, opts.max, opts.cpuPercent, opts.memoryPercent)
}

func deployFromFlags(cmd *cobra.Command, workloadName string, opts *options) error {
	project := util.ProjectFromCmd(cmd)
	if project == "" {
		return fmt.Errorf("no project set — pass --project or run 'datumctl config set project <name>'")
	}
	if opts.image == "" {
		return fmt.Errorf("--image is required")
	}
	locationSelector, err := resolveLocationSelector(opts)
	if err != nil {
		return err
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

	// Resolve the HTTP service this deploy declares:
	//
	//	--http-port N  declares (or changes) it
	//	--no-http      removes it, and the URL with it
	//	neither        keeps what the workload already declares
	//
	// Carrying the existing port forward matters: without it, a routine image
	// bump would drop the port from the spec and take a live URL down without
	// anyone saying so. --no-http is the only way to stop serving.
	httpPort := opts.httpPort
	if httpPort == 0 && !opts.noHTTP {
		httpPort = declaredHTTPPort(&workload)
	}

	// Build spec.
	tcp := corev1.ProtocolTCP
	container := computev1alpha.SandboxContainer{
		Name:  "app",
		Image: opts.image,
	}
	portName := ""
	if httpPort > 0 {
		httpNamedPort := computev1alpha.NamedPort{Name: httpPortName, Port: httpPort, Protocol: &tcp}
		portName, err = url.PortName(httpNamedPort)
		if err != nil {
			return err
		}
		container.Ports = []computev1alpha.NamedPort{httpNamedPort}
	}

	locationRefs := make([]locationsv1alpha1.LocationReference, 0, len(locations))
	for _, name := range locations {
		locationRefs = append(locationRefs, locationsv1alpha1.LocationReference{Name: name})
	}

	// All locations go into one "default" placement, replacing whatever
	// placements the workload had before.
	scaleSettings, err := resolveScaleSettings(cmd, out, opts, workload.Spec.Placements)
	if err != nil {
		return err
	}

	placement := computev1alpha.WorkloadPlacement{
		Name:             "default",
		Locations:        locationRefs,
		LocationSelector: locationSelector,
		ScaleSettings:    scaleSettings,
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

	fmt.Fprintln(out, planLine(`Placement "default"`,
		fmt.Sprintf("%s, %s", describePlacementLocations(placement), util.FormatScaleSettings(scaleSettings))))

	removedURL := planHTTPService(ctx, out, c, workloadName, httpPort, opts, creating)

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

	if opts.noHTTP {
		if err := removeHTTPService(ctx, out, c, workloadName, removedURL, creating); err != nil {
			return err
		}
	}

	// The URL goes in alongside the workload, not after the rollout: backends
	// register as instances come up, so the URL answers moments after the last
	// city is Done rather than starting from scratch once it is.
	//
	// A failure is carried to publish rather than returned here. The workload
	// is applied and rolling out, and a user is owed that table before being
	// told the URL did not go up.
	publishErr := declareURL(ctx, c, &workload, portName, httpPort)

	// Save workload.yaml.
	if err := saveWorkloadYAML(workloadName, &workload); err != nil {
		fmt.Fprintf(out, "  warning: could not save workload.yaml: %v\n", err)
	} else {
		_, _ = fmt.Fprintln(out, "Saved workload.yaml")
	}

	fmt.Fprintf(out, "Waiting for rollout. Ctrl-C to detach (rollout continues in background).\n\n")

	watchCtx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer cancel()
	if err := watch.Rollout(watchCtx, c, out, project, workload.UID); err != nil {
		return err
	}

	return publish(watchCtx, out, c, &workload, httpPort, opts, publishErr)
}

// planHTTPService prints the HTTP line of the plan summary and returns the URL
// that --no-http is about to take down, if there is one.
//
// The HTTP service is part of the plan, not a surprise after the fact: what
// gets published — or what stops answering — is stated before the prompt that
// creates it.
func planHTTPService(ctx context.Context, out io.Writer, c client.Client, workloadName string, httpPort int32, opts *options, creating bool) string {
	if httpPort > 0 {
		fmt.Fprintln(out, planLine("HTTP service", fmt.Sprintf("port %d → Datum-managed URL", httpPort)))
		// Said here, once, because a container that terminates TLS itself
		// answers nothing and the only symptom is a URL that does not work.
		fmt.Fprintln(out, planNote("Datum terminates TLS; serve plain HTTP on this port."))
		return ""
	}
	if !opts.noHTTP || creating {
		return ""
	}

	// A lookup failure here is not worth failing a deploy over: the line just
	// loses the hostname it would have named, and Unpublish reports any real
	// problem with the control plane a moment later.
	removedURL := ""
	if info, err := url.ForWorkload(ctx, c, workloadName); err == nil && info != nil {
		removedURL = info.URL
	}

	if removedURL == "" {
		fmt.Fprintln(out, planLine("HTTP service", "removed"))
		return ""
	}
	fmt.Fprintln(out, planLine("HTTP service", fmt.Sprintf("removed — %s will stop responding", removedURL)))
	return removedURL
}

// removeHTTPService takes the URL down. It runs as soon as the workload stops
// declaring the port rather than at the end of the rollout: the workload and
// what answers for it have to agree.
func removeHTTPService(ctx context.Context, out io.Writer, c client.Client, workloadName, removedURL string, creating bool) error {
	if err := url.Unpublish(ctx, c, workloadName); err != nil {
		return err
	}
	switch {
	case removedURL != "":
		fmt.Fprintf(out, "  HTTP service removed — %s no longer responds\n", removedURL)
	case !creating:
		_, _ = fmt.Fprintln(out, "  HTTP service removed")
	}
	return nil
}

// declareURL writes the objects that put the workload on its URL. It runs
// alongside the workload write, before the rollout: backends then register as
// instances come up, and the URL is ready within a second or two of the last
// city reaching Done. Declaring them after the rollout would add a visible
// stall to every deploy.
//
// It prints nothing. Nothing has happened yet that a user needs to read, and
// the rollout table comes next; publishing reports itself once the rollout is
// over and there is progress to show.
func declareURL(ctx context.Context, c client.Client, w *computev1alpha.Workload, portName string, port int32) error {
	if port <= 0 {
		return nil
	}

	hostnames, err := existingHostnames(ctx, c, w.Name)
	if err != nil {
		return err
	}
	return url.Declare(ctx, c, w, portName, port, hostnames)
}

// notReachable states the dead end this whole feature exists to close: a
// workload with no HTTP port is not on the internet, and no developer should
// have to work that out for themselves.
func notReachable(out io.Writer, workloadName string) {
	fmt.Fprintf(out, "\n  No HTTP port declared — this workload is not reachable from the internet.\n")
	fmt.Fprintf(out, "  To publish it:  datumctl compute deploy %s --http-port 8080\n", workloadName)
}

// publish waits for the URL declared before the rollout and prints it as the
// last line of the deploy, or explains why there is no URL to print.
//
// declareErr is whatever declareURL reported. It is carried this far rather
// than failing the deploy on the spot so that a user still gets the rollout
// table for a workload that is, after all, being deployed.
//
// It runs after the rollout, so the workload is already up: a failure here is
// a failure to publish, never a failure to deploy, and it says so before the
// error is returned. A user whose workload is running must not read a bare
// "Error:" as "the deploy failed".
func publish(ctx context.Context, out io.Writer, c client.Client, w *computev1alpha.Workload, port int32, opts *options, declareErr error) error {
	if port <= 0 {
		// --no-http was just told, line by line, that the URL is gone. Telling
		// the same user to publish is answering a question nobody asked.
		if !opts.noHTTP {
			notReachable(out, w.Name)
		}
		return nil
	}

	_, _ = fmt.Fprintln(out, "\nPublishing...")

	// The objects went in before the rollout, so there is nothing left to do
	// here but watch — including for a user who detached, whose URL is already
	// declared and coming up without them.
	var info *url.Info
	err := declareErr
	if err == nil {
		info, err = url.Wait(ctx, out, c, w.Name)
	}
	if err != nil {
		fmt.Fprintf(out, "\n  The rollout succeeded — the workload is deployed and running.\n")
		fmt.Fprintf(out, "  Only publishing its URL failed. Retry with:\n")
		fmt.Fprintf(out, "    datumctl compute deploy %s --image %s --http-port %d\n", w.Name, opts.image, port)
		return fmt.Errorf("publishing URL for workload %q: %w", w.Name, err)
	}

	// A nil Info is a detach, not a failure: url.Wait has already said how to
	// pick the URL up again.
	if info == nil || info.URL == "" {
		return nil
	}

	fmt.Fprintf(out, "\n  %s\n", info.URL)
	return nil
}

// existingHostnames returns the custom hostnames already attached to the
// workload's URL, so republishing carries them forward.
//
// Publishing rewrites the proxy spec wholesale. Custom hostnames are not set by
// this plugin — they are configured out of band, by the ALB tooling that owns
// advanced proxy configuration — so without this every redeploy would silently
// detach them and the custom domain would stop answering. That matters more,
// not less, for hostnames this plugin cannot see itself having added.
//
// It fails closed. A workload that has never been published has no hostnames
// and that is a nil with no error, but a control plane that cannot be read is
// an error the caller must stop on: the two calls use different verbs on the
// same object — a List here, a Get in the apply — so a control plane that
// refuses one and answers the other would otherwise rewrite spec.Hostnames to
// nothing and report success.
func existingHostnames(ctx context.Context, c client.Client, workloadName string) ([]string, error) {
	info, err := url.ForWorkload(ctx, c, workloadName)
	if err != nil {
		return nil, fmt.Errorf("reading the domains attached to %q: %w", workloadName, err)
	}
	if info == nil {
		return nil, nil
	}
	return info.CustomHostnames, nil
}

// planLine renders one line of the plan summary printed before the Apply
// prompt, with every value starting in the same column.
func planLine(label, value string) string {
	return fmt.Sprintf("  %-*s %s", planLabelWidth, label+":", value)
}

// planNote renders a continuation of the plan line above it, aligned under
// that line's value rather than carrying a label of its own.
func planNote(text string) string {
	return fmt.Sprintf("  %-*s %s", planLabelWidth, "", text)
}

// declaredHTTPPort returns the HTTP port a workload already declares, or 0.
// The port named "http" wins; failing that, the first declared port is the one
// the URL was built on, since that is what a flag-driven deploy writes.
func declaredHTTPPort(w *computev1alpha.Workload) int32 {
	sandbox := w.Spec.Template.Spec.Runtime.Sandbox
	if sandbox == nil {
		return 0
	}
	first := int32(0)
	for _, container := range sandbox.Containers {
		for _, p := range container.Ports {
			if p.Name == httpPortName {
				return p.Port
			}
			if first == 0 {
				first = p.Port
			}
		}
	}
	return first
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

	if err := watch.Rollout(watchCtx, c, out, project, workload.UID); err != nil {
		return err
	}

	reportManifestReachability(out, &workload)
	return nil
}

// reportManifestReachability closes the dead end for the manifest path.
//
// TODO: the manifest path publishes nothing. A workload manifest has no way to
// declare "this is an HTTP service" — the flag path's --http-port has no
// equivalent field — and inferring one from a container port would publish
// workloads whose authors never asked for a URL. Resolving it means a field on
// the workload spec, which is an API decision, not a CLI one.
//
// The note, though, is not publishing. A workload nothing can reach is the
// same dead end however it was deployed, and a developer who reads it after a
// flag deploy but not after a -f deploy is a developer who concludes the URL
// is somewhere they have not looked.
func reportManifestReachability(out io.Writer, w *computev1alpha.Workload) {
	if declaredHTTPPort(w) == 0 {
		notReachable(out, w.Name)
	}
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
			if maxReplicasStr(op.ScaleSettings.MaxReplicas) != maxReplicasStr(np.ScaleSettings.MaxReplicas) {
				lines = append(lines, fmt.Sprintf("  placement %q max replicas: %s → %s",
					name, maxReplicasStr(op.ScaleSettings.MaxReplicas), maxReplicasStr(np.ScaleSettings.MaxReplicas)))
			}
			if len(op.ScaleSettings.Metrics) != len(np.ScaleSettings.Metrics) {
				lines = append(lines, fmt.Sprintf("  placement %q autoscaling metrics: %d → %d",
					name, len(op.ScaleSettings.Metrics), len(np.ScaleSettings.Metrics)))
			}
			if before, after := describePlacementLocations(op), describePlacementLocations(np); before != after {
				lines = append(lines, fmt.Sprintf("  placement %q: %s → %s", name, before, after))
			}
		} else {
			lines = append(lines, fmt.Sprintf("  + new placement %q: %s", name, describePlacementLocations(np)))
		}
	}
	for name := range oldPlacements {
		if _, ok := newPlacements[name]; !ok {
			lines = append(lines, fmt.Sprintf("  - removed placement %q", name))
		}
	}

	return lines
}

// maxReplicasStr renders a placement's MaxReplicas for diff output.
func maxReplicasStr(max *int32) string {
	if max == nil {
		return "none"
	}
	return fmt.Sprintf("%d", *max)
}

// describePlacementLocations says where a placement runs the way the CLI
// prints it: the locations it names, or the selector it resolves through.
func describePlacementLocations(p computev1alpha.WorkloadPlacement) string {
	if p.LocationSelector != nil {
		return fmt.Sprintf("selector=[%s]", metav1.FormatLabelSelector(p.LocationSelector))
	}
	if len(p.CityCodes) > 0 {
		// Stored before placement moved to locations and not yet rewritten.
		return fmt.Sprintf("cities=[%s]", strings.Join(p.CityCodes, ", "))
	}
	names := make([]string, 0, len(p.Locations))
	for _, ref := range p.Locations {
		names = append(names, ref.Name)
	}
	return fmt.Sprintf("locations=[%s]", strings.Join(names, ", "))
}
