package workloads

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/url"
	"go.datum.net/compute/internal/cmd/compute/util"
)

// Command returns the top-level "workloads" command group.
func Command() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workloads",
		Short: "List or inspect workloads",
		Long: `List all workloads in the project, optionally filtered by health or location.
Use the describe subcommand for a unified config + health view of a single workload.

The URL column shows where a workload answers on the public internet; a
workload that declares no HTTP port shows "—", and "?" means the URLs could not
be read at all. JSON and YAML output carry the same value as a "url" field,
alongside the whole workload resource under "workload"; there the two cases are
told apart by omitting "url" and, when the read failed, setting "urlError".

The LOCATIONS column lists the locations a workload is placed in, shortened when
there are many; JSON and YAML always carry the full list under "locations".`,
		Example: `  # List all workloads
  datumctl compute workloads

  # Filter by health
  datumctl compute workloads --health=degraded

  # Filter by location
  datumctl compute workloads --location=us-east-1

  # Machine-readable output
  datumctl compute workloads -o json

  # One workload's URL, for scripting
  datumctl compute workloads -o json | jq -r '.[] | select(.name=="api") | .url'

  # Describe a single workload
  datumctl compute workloads describe api`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(cmd, args)
		},
	}

	// List flags.
	cmd.Flags().String("health", "", "Filter by health: available, degraded, progressing, unknown")
	cmd.Flags().String("location", "", "Filter to workloads with a placement in this location")
	cmd.Flags().StringP("output", "o", "table", "Output format: table, wide, json, yaml")
	cmd.Flags().Bool("no-headers", false, "Omit the table header row (table and wide only)")

	_ = cmd.RegisterFlagCompletionFunc("health", util.CompleteOutputFormats("available", "degraded", "progressing", "unknown"))
	_ = cmd.RegisterFlagCompletionFunc("location", util.CompleteLocations)
	_ = cmd.RegisterFlagCompletionFunc("output", util.CompleteOutputFormats("table", "wide", "json", "yaml"))

	cmd.AddCommand(describeCommand())

	return cmd
}

// -----------------------------------------------------------------------
// workloads list
// -----------------------------------------------------------------------

const (
	// noURL is what the table shows for a workload that is not published.
	noURL = "—"
	// unknownURL is what the table shows when the URLs could not be read at
	// all, which is a different thing from a workload having none.
	unknownURL = "?"
	// maxTableLocations is how many locations the LOCATIONS column names before
	// it summarises the rest; a workload in a dozen locations must not push the
	// columns to its right off the terminal.
	maxTableLocations = 3
)

// listOptions are the parsed flags of the list view.
type listOptions struct {
	output    util.OutputFormat
	health    string
	location  string
	noHeaders bool
}

// workloadRow is one rendered line of the list view.
type workloadRow struct {
	name        string
	health      string
	healthShort string // first word: the narrow table column, and the filter key
	ready       string
	upToDate    string
	placements  []string
	locations   []string
	image       string
	age         string
	instType    string
	url         string // "" when the workload has no URL
	workload    *computev1alpha.Workload
}

// workloadView is the machine-readable form of a row.
//
// `-o json` / `-o yaml` used to emit the raw WorkloadList. They now emit a
// top-level array of these, because a workload's URL is not a field of a
// Workload — it lives on separate objects — and the contract is that
// `workloads -o json | jq -r '.[] | select(.name=="api") | .url'` works.
//
// No existing field changed shape or meaning: the raw resource is carried
// whole under `workload`, so `.items[].spec` becomes `.[].workload.spec`.
type workloadView struct {
	Name         string   `json:"name"`
	Health       string   `json:"health"`
	Ready        string   `json:"ready"`
	UpToDate     string   `json:"upToDate"`
	Placements   []string `json:"placements,omitempty"`
	Locations    []string `json:"locations,omitempty"`
	Image        string   `json:"image,omitempty"`
	InstanceType string   `json:"instanceType,omitempty"`
	Age          string   `json:"age,omitempty"`

	// URL is where the workload answers, and is omitted when it has none —
	// the structured form of the table's "—". It is also omitted when the
	// URLs could not be read, and then URLError carries the server's error,
	// which is the structured form of the table's "?". Reporting both cases
	// as `"url": ""` would tell a consumer a workload is unpublished when
	// all that actually happened is that the lookup failed.
	URL      string `json:"url,omitempty"`
	URLError string `json:"urlError,omitempty"`

	Workload *computev1alpha.Workload `json:"workload,omitempty"`
}

func runList(cmd *cobra.Command, _ []string) error {
	project := util.ProjectFromCmd(cmd)

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	outputFlag, _ := cmd.Flags().GetString("output")
	healthFilter, _ := cmd.Flags().GetString("health")
	locationFilter, _ := cmd.Flags().GetString("location")
	noHeaders, _ := cmd.Flags().GetBool("no-headers")

	return listWorkloads(context.Background(), cmd.OutOrStdout(), cmd.ErrOrStderr(), c, project, listOptions{
		output:    util.OutputFormat(outputFlag),
		health:    healthFilter,
		location:  locationFilter,
		noHeaders: noHeaders,
	})
}

// listWorkloads renders the list view. The client is a parameter so the whole
// view can be rendered against a fake one in tests.
func listWorkloads(ctx context.Context, out, errOut io.Writer, c client.Client, project string, opts listOptions) error {
	result, err := collectRows(ctx, errOut, c, opts)
	if err != nil {
		return err
	}

	switch opts.output {
	case util.OutputJSON:
		return util.PrintJSON(out, viewsOf(result))
	case util.OutputYAML:
		return util.PrintYAML(out, viewsOf(result))
	}

	if len(result.rows) == 0 {
		printNoRows(out, project, opts)
		return nil
	}

	renderTable(out, result, opts)
	return nil
}

// listing is what reading the project produced: the rows the filters left
// standing, plus the URL lookup's own failure if it had one. A non-nil urlErr
// means every row's URL is unknown, which is not the same claim as a workload
// having none — both the table and the structured output draw that line.
type listing struct {
	rows   []workloadRow
	urlErr error
}

// urlsKnown reports whether the project's URLs could be read at all.
func (l listing) urlsKnown() bool { return l.urlErr == nil }

// collectRows reads the project and assembles the rows the filters left
// standing. Its error is the command failing outright; a URL lookup that fails
// is carried on the listing instead, because the URL is a column, not the
// command.
func collectRows(ctx context.Context, errOut io.Writer, c client.Client, opts listOptions) (listing, error) {
	var wlList computev1alpha.WorkloadList
	if err := c.List(ctx, &wlList, client.InNamespace(util.ResourceNamespace)); err != nil {
		return listing{}, fmt.Errorf("listing workloads: %w", err)
	}

	var deployList computev1alpha.WorkloadDeploymentList
	if err := c.List(ctx, &deployList, client.InNamespace(util.ResourceNamespace)); err != nil {
		return listing{}, fmt.Errorf("listing deployments: %w", err)
	}

	// URLs come in one pass for the whole project rather than a lookup per
	// workload. A project whose URLs cannot be read still lists its workloads.
	urls, urlErr := url.ForAll(ctx, c)
	if urlErr != nil {
		fmt.Fprintf(errOut, "Warning: could not read URLs: %v\n", urlErr)
	}

	// workloadUID → its deployments, and the set of UIDs with a deployment in
	// the requested city.
	deploysByWorkload := make(map[string][]computev1alpha.WorkloadDeployment)
	locationFilteredUIDs := map[string]bool{}
	for _, d := range deployList.Items {
		wUID := d.Labels[computev1alpha.WorkloadUIDLabel]
		deploysByWorkload[wUID] = append(deploysByWorkload[wUID], d)
		if opts.location != "" && d.Spec.LocationRef.Name == opts.location {
			locationFilteredUIDs[wUID] = true
		}
	}

	var rows []workloadRow

	for i := range wlList.Items {
		wl := &wlList.Items[i]

		if opts.location != "" && !locationFilteredUIDs[string(wl.UID)] {
			continue
		}

		var totalReady, totalUpdated, totalDesired int32
		for _, d := range deploysByWorkload[string(wl.UID)] {
			totalReady += d.Status.ReadyReplicas
			totalUpdated += d.Status.UpdatedReplicas
			totalDesired += d.Status.DesiredReplicas
		}

		health := util.WorkloadHealth(wl.Status.Conditions, totalReady, totalDesired)
		healthShort := strings.SplitN(health, " ", 2)[0] // e.g. "Available", "Degraded"

		if opts.health != "" && !strings.EqualFold(healthShort, opts.health) {
			continue
		}

		image := ""
		if wl.Spec.Template.Spec.Runtime.Sandbox != nil &&
			len(wl.Spec.Template.Spec.Runtime.Sandbox.Containers) > 0 {
			image = wl.Spec.Template.Spec.Runtime.Sandbox.Containers[0].Image
		}

		rows = append(rows, workloadRow{
			name:        wl.Name,
			health:      health,
			healthShort: healthShort,
			ready:       fmt.Sprintf("%d/%d", totalReady, totalDesired),
			upToDate:    fmt.Sprintf("%d/%d", totalUpdated, totalDesired),
			placements:  placementNames(wl),
			locations:   placementLocations(wl),
			image:       image,
			age:         util.RelativeAge(wl.CreationTimestamp),
			instType:    wl.Spec.Template.Spec.Runtime.Resources.InstanceType,
			url:         urlOf(urls[wl.Name]),
			workload:    wl,
		})
	}

	return listing{rows: rows, urlErr: urlErr}, nil
}

// urlOf is the one URL to show for a workload, or "" when it has none. The
// url package has already picked between a custom hostname and the
// platform-managed one; a nil Info is an unpublished workload.
func urlOf(info *url.Info) string {
	if info == nil {
		return ""
	}
	return info.URL
}

// placementNames lists the workload's placements in declared order.
func placementNames(wl *computev1alpha.Workload) []string {
	names := make([]string, 0, len(wl.Spec.Placements))
	for _, p := range wl.Spec.Placements {
		names = append(names, p.Name)
	}
	return names
}

// placementLocations lists every location the workload places into,
// deduplicated, in declared order.
//
// A placement can name its locations, resolve them through a topology
// selector, or still carry city codes stored before placement moved to
// locations. A selector cannot be expanded without asking the server, so it is
// reported as the selector itself — the same thing describe shows.
func placementLocations(wl *computev1alpha.Workload) []string {
	var locations []string
	seen := map[string]bool{}
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			locations = append(locations, name)
		}
	}
	for _, p := range wl.Spec.Placements {
		for _, ref := range p.Locations {
			add(ref.Name)
		}
		if len(p.Locations) == 0 {
			if p.LocationSelector != nil {
				add("selector: " + metav1.FormatLabelSelector(p.LocationSelector))
				continue
			}
			for _, city := range p.CityCodes {
				add(city)
			}
		}
	}
	return locations
}

// viewsOf converts rows to their machine-readable form. It always returns a
// non-nil slice so an empty project encodes as [] rather than null — `jq` over
// an empty project should iterate nothing, not fail.
func viewsOf(l listing) []workloadView {
	// The lookup failed for the project, so it failed for every row: none of
	// their URLs is known, and saying nothing at all would read as "none".
	urlError := ""
	if l.urlErr != nil {
		urlError = l.urlErr.Error()
	}

	views := make([]workloadView, 0, len(l.rows))
	for _, r := range l.rows {
		views = append(views, workloadView{
			Name:         r.name,
			Health:       r.health,
			Ready:        r.ready,
			UpToDate:     r.upToDate,
			Placements:   r.placements,
			Locations:    r.locations,
			Image:        r.image,
			InstanceType: r.instType,
			Age:          r.age,
			URL:          r.url,
			URLError:     urlError,
			Workload:     r.workload,
		})
	}
	return views
}

func renderTable(out io.Writer, l listing, opts listOptions) {
	wide := opts.output == util.OutputWide
	urlsKnown := l.urlsKnown()

	tw := util.NewTabWriter(out)
	if !opts.noHeaders {
		if wide {
			fmt.Fprintf(tw, "NAME\tLOCATIONS\tHEALTH\tREADY\tUP-TO-DATE\tPLACEMENTS\tIMAGE\tAGE\tINSTANCE TYPE\tURL\n")
		} else {
			fmt.Fprintf(tw, "NAME\tLOCATIONS\tHEALTH\tREADY\tUP-TO-DATE\tPLACEMENTS\tIMAGE\tAGE\tURL\n")
		}
	}
	for _, r := range l.rows {
		if wide {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.name, locationsColumn(r.locations), r.healthShort, r.ready, r.upToDate,
				columnOrNone(r.placements), truncateImage(r.image), r.age, r.instType,
				urlColumn(r.url, urlsKnown))
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				r.name, locationsColumn(r.locations), r.healthShort, r.ready, r.upToDate,
				columnOrNone(r.placements), truncateImage(r.image), r.age,
				urlColumn(r.url, urlsKnown))
		}
	}
	_ = tw.Flush()

	fmt.Fprintf(out, "\n%s\n", healthSummary(l.rows))
}

// locationsColumn renders the LOCATIONS cell. A workload can be placed in more
// locations than a terminal column can hold, so past maxTableLocations the cell
// names the first few and counts the rest; `-o json` still carries all of
// them under "locations".
func locationsColumn(locations []string) string {
	if len(locations) <= maxTableLocations {
		return columnOrNone(locations)
	}
	return fmt.Sprintf("%s (+%d)", strings.Join(locations[:maxTableLocations], ", "), len(locations)-maxTableLocations)
}

// urlColumn renders the URL cell: the URL, "—" when the workload has none, and
// "?" when the project's URLs could not be read at all.
func urlColumn(u string, urlsKnown bool) string {
	switch {
	case u != "":
		return u
	case !urlsKnown:
		return unknownURL
	default:
		return noURL
	}
}

// columnOrNone joins a list for a table cell, or says so when it is empty.
func columnOrNone(values []string) string {
	if len(values) == 0 {
		return "(none)"
	}
	return strings.Join(values, ", ")
}

// healthSummary tallies health across the rows that survived filtering.
func healthSummary(rows []workloadRow) string {
	counts := map[string]int{}
	for _, r := range rows {
		switch r.healthShort {
		case "Available", "Degraded", "Unavailable":
			counts[r.healthShort]++
		default:
			counts["Unknown"]++
		}
	}
	return fmt.Sprintf("%d workloads — %d Available, %d Degraded, %d Unavailable, %d Unknown",
		len(rows), counts["Available"], counts["Degraded"], counts["Unavailable"], counts["Unknown"])
}

// printNoRows explains an empty table in terms of whatever the user asked for.
func printNoRows(out io.Writer, project string, opts listOptions) {
	switch {
	case opts.health != "":
		fmt.Fprintf(out, "No workloads in project %s match health=%s.\n", project, opts.health)
	case opts.location != "":
		fmt.Fprintf(out, "No workloads in project %s have a placement in location %s.\n", project, opts.location)
	default:
		fmt.Fprintf(out, "No workloads found in project %s.\n\n", project)
		fmt.Fprintf(out, "Get started:\n")
		fmt.Fprintf(out, "  datumctl compute deploy api --image=ghcr.io/acme/api:v1.0.0 --location=us-east-1\n")
	}
}

// truncateImage strips the registry host from an image reference so the table
// column stays compact.  "ghcr.io/acme/api:v1" → "acme/api:v1".
func truncateImage(image string) string {
	if image == "" {
		return "(vm)"
	}
	parts := strings.SplitN(image, "/", 2)
	if len(parts) == 2 {
		// Only strip the first component if it looks like a registry host
		// (contains a '.' or ':' — as opposed to a Docker Hub org name).
		host := parts[0]
		if strings.ContainsAny(host, ".:") {
			return parts[1]
		}
	}
	return image
}

// -----------------------------------------------------------------------
// workloads describe
// -----------------------------------------------------------------------

func describeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "describe <name>",
		Short: "Show config and health for a single workload",
		Long: `Display a unified view of workload configuration (container spec, scale settings)
and runtime health (per-location ready/desired counts). Replaces 'datumctl compute status'.`,
		Args:    cobra.ExactArgs(1),
		Example: `  datumctl compute workloads describe api`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDescribe(cmd, args)
		},
		ValidArgsFunction: util.CompleteWorkloadNames,
	}

	cmd.Flags().StringP("output", "o", "wide", "Output format: wide, json, yaml")
	_ = cmd.RegisterFlagCompletionFunc("output", util.CompleteOutputFormats("wide", "json", "yaml"))

	return cmd
}

func runDescribe(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	project := util.ProjectFromCmd(cmd)
	outputFlag, _ := cmd.Flags().GetString("output")

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	workloadName := args[0]

	var wl computev1alpha.Workload
	if err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: workloadName}, &wl); err != nil {
		if k8serrors.IsNotFound(err) {
			return fmt.Errorf("workload %q not found in project %s", workloadName, project)
		}
		return fmt.Errorf("getting workload: %w", err)
	}

	// JSON / YAML: emit the raw resource.
	switch util.OutputFormat(outputFlag) {
	case util.OutputJSON:
		return util.PrintJSON(cmd.OutOrStdout(), &wl)
	case util.OutputYAML:
		return util.PrintYAML(cmd.OutOrStdout(), &wl)
	}

	// List deployments for this workload.
	selector := labels.SelectorFromSet(labels.Set{computev1alpha.WorkloadUIDLabel: string(wl.UID)})
	var deployList computev1alpha.WorkloadDeploymentList
	if err := c.List(ctx, &deployList, client.InNamespace(util.ResourceNamespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return fmt.Errorf("listing deployments: %w", err)
	}

	// Compute totals for health.
	var totalDesired, totalReady int32
	for _, d := range deployList.Items {
		totalDesired += d.Status.DesiredReplicas
		totalReady += d.Status.ReadyReplicas
	}

	health := util.WorkloadHealth(wl.Status.Conditions, totalReady, totalDesired)

	// Determine type label.
	typeLabel := "virtual-machine"
	if wl.Spec.Template.Spec.Runtime.Sandbox != nil {
		instType := wl.Spec.Template.Spec.Runtime.Resources.InstanceType
		if instType != "" {
			typeLabel = "sandbox/" + instType
		} else {
			typeLabel = "sandbox"
		}
	}

	age := util.RelativeAgeVerbose(wl.CreationTimestamp)

	out := cmd.OutOrStdout()

	// Header block.
	fmt.Fprintf(out, "%-12s %-31s project: %s\n", "Workload", workloadName, project)
	fmt.Fprintf(out, "%-12s %s\n", "Type", typeLabel)
	fmt.Fprintf(out, "%-12s %s\n", "Updated", age)
	fmt.Fprintf(out, "\n")
	fmt.Fprintf(out, "%-12s %s\n", "Health", health)
	fmt.Fprintf(out, "\n")

	// URL block. A workload that was never published simply has no URL, so a
	// lookup failure is reported and skipped rather than failing the whole
	// describe — the config and health above are still what the user asked for.
	if info, err := url.ForWorkload(ctx, c, workloadName); err != nil {
		fmt.Fprintf(out, "URL\n  (could not be read: %v)\n\n", err)
	} else if info != nil {
		url.RenderDetail(out, info)
		fmt.Fprintf(out, "\n")
	}

	// Placements block.
	fmt.Fprintf(out, "Placements\n")
	if len(wl.Spec.Placements) == 0 {
		fmt.Fprintf(out, "  (none configured — workload will not run anywhere)\n")
	} else {
		// Build a map: placementName → []WorkloadDeployment.
		deplsByPlacement := make(map[string][]computev1alpha.WorkloadDeployment)
		for _, d := range deployList.Items {
			deplsByPlacement[d.Spec.PlacementName] = append(deplsByPlacement[d.Spec.PlacementName], d)
		}

		for _, p := range wl.Spec.Placements {
			// Placement header line.
			maxStr := "∞"
			if p.ScaleSettings.MaxReplicas != nil {
				maxStr = fmt.Sprintf("%d", *p.ScaleSettings.MaxReplicas)
			}
			fmt.Fprintf(out, "  %-10s %-34s scale: %d..%s%s\n",
				p.Name, placementLocationsSummary(p), p.ScaleSettings.MinReplicas, maxStr, autoscaleAnnotation(p.ScaleSettings))

			// Per-location lines from deployments.
			for _, d := range deplsByPlacement[p.Name] {
				readyStr := fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, d.Status.DesiredReplicas)
				annotation := ""
				if d.Status.ReadyReplicas < d.Status.DesiredReplicas {
					// Read blocking reason from the deployment's own condition.
					annotation = degradedAnnotation(ctx, c, d)
				}
				if annotation != "" {
					fmt.Fprintf(out, "    %-8s ready: %-10s %s\n", d.Spec.LocationRef.Name, readyStr, annotation)
				} else {
					fmt.Fprintf(out, "    %-8s ready: %s\n", d.Spec.LocationRef.Name, readyStr)
				}
			}
		}
	}
	fmt.Fprintf(out, "\n")

	// Container block (sandbox only).
	if wl.Spec.Template.Spec.Runtime.Sandbox != nil && len(wl.Spec.Template.Spec.Runtime.Sandbox.Containers) > 0 {
		ctr := wl.Spec.Template.Spec.Runtime.Sandbox.Containers[0]
		fmt.Fprintf(out, "Container\n")
		fmt.Fprintf(out, "  %-10s %s\n", "Image", ctr.Image)

		if len(ctr.Ports) > 0 {
			var portStrs []string
			for _, p := range ctr.Ports {
				proto := "TCP"
				if p.Protocol != nil {
					proto = string(*p.Protocol)
				}
				portStrs = append(portStrs, fmt.Sprintf("%d/%s", p.Port, proto))
			}
			fmt.Fprintf(out, "  %-10s %s\n", "Ports", strings.Join(portStrs, ", "))
		}

		if len(ctr.Env) > 0 {
			fmt.Fprintf(out, "  Env\n")
			for _, e := range ctr.Env {
				fmt.Fprintf(out, "    %s\n", formatEnvVar(e))
			}
		}

		// Resources.
		instType := wl.Spec.Template.Spec.Runtime.Resources.InstanceType
		if instType != "" {
			fmt.Fprintf(out, "  %-10s %s\n", "Resources", instType)
		}

		fmt.Fprintf(out, "\n")
	}

	// Next steps.
	fmt.Fprintf(out, "Next steps:\n")
	fmt.Fprintf(out, "  %-25s datumctl compute instances --workload=%s\n", "List instances:", workloadName)
	fmt.Fprintf(out, "  %-25s datumctl compute logs <instance>\n", "Stream logs:")
	fmt.Fprintf(out, "  %-25s datumctl compute rollout undo %s\n", "Roll back:", workloadName)

	return nil
}

// placementLocationsSummary says where a placement runs: the locations it
// names, or the topology selector it resolves through.
func placementLocationsSummary(p computev1alpha.WorkloadPlacement) string {
	if p.LocationSelector != nil {
		return "selector: " + metav1.FormatLabelSelector(p.LocationSelector)
	}
	if len(p.CityCodes) > 0 {
		// Stored before placement moved to locations and not yet rewritten.
		return "cities: " + strings.Join(p.CityCodes, ", ")
	}
	names := make([]string, 0, len(p.Locations))
	for _, ref := range p.Locations {
		names = append(names, ref.Name)
	}
	return "locations: " + strings.Join(names, ", ")
}

// autoscaleAnnotation renders a placement's autoscaling metrics for the
// "scale: min..max" line, e.g. " (cpu@70%, memory@80%)". The HPA controller
// only acts when a metric is set too, so a max with none is flagged rather
// than shown as if autoscaling were active.
func autoscaleAnnotation(s computev1alpha.HorizontalScaleSettings) string {
	if s.MaxReplicas == nil {
		return ""
	}
	ann := util.MetricsAnnotation(s.Metrics)
	if ann == "" {
		return " (autoscaling disabled)"
	}
	return " " + ann
}

// degradedAnnotation returns a short annotation for a per-location line when the
// deployment is not fully ready. It reads the blocking reason+message from the
// deployment's own Available condition, which the server rolls up from the
// underlying instances. No per-instance fetch or reason branching needed.
func degradedAnnotation(_ context.Context, _ client.Client, d computev1alpha.WorkloadDeployment) string {
	reason, msg, blocked := util.ReadinessBlock(d.Status.Conditions, computev1alpha.WorkloadDeploymentAvailable)
	if !blocked {
		return ""
	}
	if msg != "" {
		return "Blocked — " + msg
	}
	if reason != "" {
		return "Blocked — " + reason
	}
	return "Blocked"
}

// formatEnvVar renders a single EnvVar for display.
func formatEnvVar(e corev1.EnvVar) string {
	if e.ValueFrom != nil {
		if e.ValueFrom.SecretKeyRef != nil {
			return fmt.Sprintf("%-20s from secret %s", e.Name, e.ValueFrom.SecretKeyRef.Name)
		}
		if e.ValueFrom.ConfigMapKeyRef != nil {
			return fmt.Sprintf("%-20s from configmap %s", e.Name, e.ValueFrom.ConfigMapKeyRef.Name)
		}
	}
	return fmt.Sprintf("%-20s %s", e.Name, e.Value)
}
