// SPDX-License-Identifier: AGPL-3.0-only

// Package logs implements "datumctl compute logs", which reads a workload's
// instance logs, or its ALB access logs, from the project's telemetry API.
package logs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
)

const (
	// isolationUnikernel is the RuntimeClass isolation boundary of unikernels.
	isolationUnikernel = "unikernel"

	defaultLookback = 24 * time.Hour

	flagInstance  = "instance"
	flagLocation  = "location"
	flagContainer = "container"
	flagPrevious  = "previous"
	flagCurrent   = "current"

	outputText  = "text"
	defaultTail = 200

	// deletedLookback is how far back a missing workload's logs are looked
	// for, to tell a deleted workload from a typo.
	deletedLookback = 30 * 24 * time.Hour
)

type options struct {
	instances  []string
	locations  []string
	container  string
	search     string
	alb        bool
	follow     bool
	previous   bool
	current    bool
	since      string
	sinceTime  string
	until      string
	tail       int
	timestamps bool
	prefix     bool
	utc        bool
	output     string
	showQuery  bool
	browser    bool
}

// Command returns the "logs" command.
func Command() *cobra.Command {
	cmd, _ := newCommand()
	return cmd
}

func newCommand() (*cobra.Command, *options) {
	opts := &options{}

	cmd := &cobra.Command{
		Use:   "logs <workload-name>",
		Short: "Show logs for a workload's instances",
		Long: `Show logs for a workload, merged across every instance it runs, oldest first.

Without a window, the newest 200 lines from the last 24 hours are shown. With
--since or --since-time every line in the window is shown unless --tail says
otherwise. Logs of deleted instances, and of a deleted workload, are included.

An instance keeps its name when it is redeployed, but runs as a new
generation: a new VM or pod. Restarts in place keep the generation, so a
crash-looping instance stays in one. A marker is printed where an instance's
generation changes; --current and --previous show a single generation per
instance.

--follow keeps polling for new lines after the backlog. There is no push-based
streaming yet, so lines appear up to a couple of seconds late.

--alb shows the access logs of the workload's published URL instead.`,
		Example: `  # Follow every instance
  datumctl compute logs api -f

  # The last hour in one location, only lines containing "error"
  datumctl compute logs api --since=1h --location=us-east-1 --search=error

  # One instance, before it was last replaced or redeployed
  datumctl compute logs api --instance=dfw-us-central-1-0 --previous

  # Requests served through the workload's URL
  datumctl compute logs api --alb --since=15m

  # Machine-readable, one JSON object per line
  datumctl compute logs api -o json | jq .line

  # Open the logs in the cloud portal
  datumctl compute logs api --browser`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: util.CompleteWorkloadNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, args[0], opts)
		},
	}

	f := cmd.Flags()
	f.StringSliceVar(&opts.instances, flagInstance, nil, "Only these instances (full or short name; repeatable)")
	f.StringSliceVar(&opts.locations, flagLocation, nil, "Only instances in these locations (repeatable)")
	f.StringVarP(&opts.container, flagContainer, "c", "", "Only this container (sandbox instances)")
	f.StringVar(&opts.search, "search", "", "Only lines containing this text, matched case-sensitively by the server")
	f.BoolVar(&opts.alb, "alb", false, "Show the access logs of the workload's published URL instead")
	f.BoolVarP(&opts.follow, "follow", "f", false, "Keep printing new lines as they arrive")
	f.BoolVar(&opts.previous, flagPrevious, false, "Only each instance's generation before the current one")
	f.BoolVar(&opts.current, flagCurrent, false, "Only each instance's current generation")
	f.StringVar(&opts.since, "since", "", "Only lines newer than this, e.g. 15m, 2h, 7d")
	f.StringVar(&opts.sinceTime, "since-time", "", "Only lines at or after this RFC3339 time")
	f.StringVar(&opts.until, "until", "", "Only lines before this RFC3339 time")
	f.IntVar(&opts.tail, "tail", defaultTail, "Newest lines to show, -1 for all (default 200, or all with --since/--since-time)")
	f.BoolVar(&opts.timestamps, "timestamps", true, "Show each line's timestamp")
	f.BoolVar(&opts.prefix, "prefix", true, "Prefix each line with its instance")
	f.BoolVar(&opts.utc, "utc", false, "Show timestamps in UTC")
	f.StringVarP(&opts.output, "output", "o", outputText, "Output format: text, json")
	f.BoolVar(&opts.showQuery, "show-query", false, "Print the LogQL query to stderr")
	f.BoolVar(&opts.browser, "browser", false, "Open the logs in the cloud portal instead (keeps a single --instance; other filters aren't carried over)")

	_ = cmd.RegisterFlagCompletionFunc(flagInstance, completeFromWorkload(instanceNames))
	_ = cmd.RegisterFlagCompletionFunc(flagContainer, completeFromWorkload(containerNames))
	_ = cmd.RegisterFlagCompletionFunc(flagLocation, util.CompleteLocations)
	_ = cmd.RegisterFlagCompletionFunc("output", util.CompleteOutputFormats(outputText, string(util.OutputJSON)))

	return cmd, opts
}

// window is the resolved time range and line budget.
type window struct {
	start, end time.Time
	tail       int // -1 for all
}

func validate(cmd *cobra.Command, opts *options) error {
	switch {
	case opts.since != "" && opts.sinceTime != "":
		return errors.New("--since and --since-time are mutually exclusive")
	case opts.follow && opts.until != "":
		return errors.New("--until cannot be combined with --follow, which always reads up to now")
	case opts.previous && opts.current:
		return errors.New("--previous and --current are mutually exclusive")
	case opts.follow && (opts.previous || opts.current):
		return errors.New("--follow cannot be combined with --previous or --current: a redeploy during the tail starts a new generation")
	case opts.tail < -1:
		return errors.New("--tail must be -1 (all) or a line count")
	case opts.alb && opts.search != "":
		return errors.New("--search cannot be combined with --alb: access log lines carry no text to search")
	}
	if opts.alb {
		for _, c := range []struct {
			flag string
			set  bool
		}{
			{flagInstance, len(opts.instances) > 0},
			{flagLocation, len(opts.locations) > 0},
			{flagContainer, opts.container != ""},
			{flagPrevious, opts.previous},
			{flagCurrent, opts.current},
		} {
			if c.set {
				return fmt.Errorf("--alb cannot be combined with --%s: access logs belong to the URL, not an instance", c.flag)
			}
		}
	}
	switch opts.output {
	case outputText, string(util.OutputTable):
		opts.output = outputText
	case string(util.OutputJSON):
	default:
		return fmt.Errorf("unsupported output format %q: use text or json", opts.output)
	}
	if !cmd.Flags().Changed("tail") && (opts.since != "" || opts.sinceTime != "") {
		opts.tail = -1
	}
	return nil
}

func resolveWindow(opts *options, now time.Time) (window, error) {
	w := window{end: now, tail: opts.tail}
	if opts.until != "" {
		t, err := time.Parse(time.RFC3339, opts.until)
		if err != nil {
			return w, errors.New("invalid --until: must be an RFC3339 time such as 2026-09-30T14:00:00Z")
		}
		w.end = t
	}

	switch {
	case opts.sinceTime != "":
		t, err := time.Parse(time.RFC3339, opts.sinceTime)
		if err != nil {
			return w, errors.New("invalid --since-time: must be an RFC3339 time such as 2026-09-30T14:00:00Z")
		}
		w.start = t
	case opts.since != "":
		d, err := parseSince(opts.since)
		if err != nil {
			return w, err
		}
		w.start = w.end.Add(-d)
	case opts.previous || opts.current:
		w.start = w.end.Add(-generationLookback)
	default:
		w.start = w.end.Add(-defaultLookback)
	}
	if !w.end.After(w.start) {
		return w, fmt.Errorf("the window is empty: %s is not before %s", w.start.Format(time.RFC3339), w.end.Format(time.RFC3339))
	}
	return w, nil
}

// parseSince accepts Go durations and whole days, e.g. 7d.
func parseSince(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if days, ok := strings.CutSuffix(s, "d"); ok && err != nil {
		var n int
		n, err = strconv.Atoi(days)
		d = time.Duration(n) * 24 * time.Hour
	}
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid --since %q: use a positive duration such as 15m, 2h or 7d", s)
	}
	return d, nil
}

func run(cmd *cobra.Command, workload string, opts *options) error {
	if err := validate(cmd, opts); err != nil {
		return err
	}
	project := util.ProjectFromCmd(cmd)
	if opts.browser {
		return openPortal(cmd.ErrOrStderr(), project, workload, opts.instances)
	}
	w, err := resolveWindow(opts, time.Now())
	if err != nil {
		return err
	}

	kube, err := util.NewClient(project)
	if err != nil {
		return err
	}
	logs, err := newLogsClient(project)
	if err != nil {
		return err
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	err = execute(ctx, env{kube, logs, cmd.OutOrStdout(), cmd.ErrOrStderr()}, workload, opts, w)
	if ctx.Err() != nil {
		return nil // Interrupted; stopping isn't a failure.
	}
	return err
}

type env struct {
	kube   client.Client
	logs   querier
	out    io.Writer
	status io.Writer
}

func execute(ctx context.Context, e env, workload string, opts *options, w window) error {
	p := &printer{
		out:        e.out,
		status:     e.status,
		json:       opts.output == "json",
		alb:        opts.alb,
		workload:   workload,
		timestamps: opts.timestamps,
		prefix:     opts.prefix,
		loc:        time.Local,
	}
	if opts.utc {
		p.loc = time.UTC
	}

	var sel selector
	var err error
	if opts.alb {
		sel, err = albSelector(ctx, e.kube, workload)
	} else {
		sel, err = appSelector(ctx, e, workload, opts, w, p)
	}
	if err != nil {
		return err
	}
	query := sel.String()
	if opts.showQuery {
		fmt.Fprintln(e.status, query)
	}

	// With --follow, the backlog seeds the follower so the first poll doesn't
	// reprint it.
	var f follower
	emit := p.emit
	if opts.follow {
		emit = func(en entry) {
			f.admit([]entry{en})
			p.emit(en)
		}
	}

	if w.tail >= 0 {
		entries, err := tail(ctx, e.logs, query, w.start, w.end, w.tail)
		if err != nil {
			return err
		}
		for _, en := range entries {
			emit(en)
		}
	} else if err := all(ctx, e.logs, query, w.start, w.end, emit); err != nil {
		return err
	}

	if !opts.follow {
		if p.printed == 0 {
			fmt.Fprintln(e.status, emptyHint(workload, opts.alb, w))
		}
		return nil
	}

	f.floor = f.cursor
	fmt.Fprintf(e.status, "Following logs for workload %q. Ctrl-C to stop.\n", workload)
	return follow(ctx, e.logs, query, &f, time.Now().Add(-followOverlap), p.emit, e.status)
}

func appSelector(ctx context.Context, e env, workload string, opts *options, w window, p *printer) (selector, error) {
	var sel selector
	wl, err := requireWorkload(ctx, e, workload, w.end)
	if err != nil {
		return sel, err
	}
	instances := qualify(workload, opts.instances)
	if err := checkFilters(ctx, e.kube, wl, workload, instances, opts); err != nil {
		return sel, err
	}

	sel.eq(labelWorkload, workload)
	sel.oneOf(labelInstance, instances)
	if len(opts.locations) > 0 {
		sel.re(labelInstance, locationPattern(workload, opts.locations))
	}
	if opts.container != "" {
		sel.eq(labelContainer, opts.container)
	}
	// Find generations before applying the search, or --previous would pick
	// the newest earlier generation that happened to log the text.
	if opts.previous || opts.current {
		if err := narrowToGeneration(ctx, e.logs, &sel, opts.previous, w, p); err != nil {
			return sel, err
		}
	}
	sel.search = opts.search
	return sel, nil
}

// qualify accepts instances by full name or by the short name line prefixes
// show.
func qualify(workload string, instances []string) []string {
	out := make([]string, len(instances))
	for i, inst := range instances {
		if !strings.HasPrefix(inst, workload+"-") {
			inst = workload + "-" + inst
		}
		out[i] = inst
	}
	return out
}

// requireWorkload rejects a typo but not a deleted workload, whose logs are
// often exactly what is wanted. It returns nil for a deleted workload.
func requireWorkload(ctx context.Context, e env, workload string, end time.Time) (*computev1alpha.Workload, error) {
	var wl computev1alpha.Workload
	err := e.kube.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: workload}, &wl)
	if err == nil {
		return &wl, nil
	}
	if !k8serrors.IsNotFound(err) {
		return nil, fmt.Errorf("getting workload %q: %w", workload, err)
	}
	names, err := e.logs.labelValues(ctx, labelWorkload, end.Add(-deletedLookback), end)
	if err != nil || !slices.Contains(names, workload) {
		return nil, fmt.Errorf("workload %q not found", workload)
	}
	fmt.Fprintf(e.status, "Workload %q no longer exists; showing the logs it left behind.\n", workload)
	return nil, nil
}

// checkFilters rejects filters that could only match nothing. A deleted
// workload (wl nil) has no spec to check --container against.
func checkFilters(ctx context.Context, kube client.Client, wl *computev1alpha.Workload, workload string, instances []string, opts *options) error {
	if wl != nil && opts.container != "" {
		containers := containersOf(wl)
		switch {
		case containers == nil:
			return fmt.Errorf("--container: workload %q runs as a VM, which has no containers", workload)
		// A unikernel runs its containers as one image, so its logs carry no
		// container name.
		case isolationOf(ctx, kube, wl) == isolationUnikernel:
			return fmt.Errorf("--container: workload %q runs as a unikernel, so its logs aren't split by container", workload)
		case !slices.Contains(containers, opts.container):
			return fmt.Errorf("--container: workload %q has no container %q (it has %s)", workload, opts.container, strings.Join(containers, ", "))
		}
	}
	if len(opts.locations) > 0 {
		re := regexp.MustCompile("^(?:" + locationPattern(workload, opts.locations) + ")$")
		for _, inst := range instances {
			if !re.MatchString(inst) {
				return fmt.Errorf("--instance %s is not in --location %s", strings.TrimPrefix(inst, workload+"-"), strings.Join(opts.locations, ", "))
			}
		}
	}
	return nil
}

// isolationOf returns the isolation boundary of the workload's runtime class,
// or "" if it can't be read.
func isolationOf(ctx context.Context, kube client.Client, wl *computev1alpha.Workload) string {
	var classes computev1alpha.RuntimeClassList
	if err := kube.List(ctx, &classes); err != nil {
		return ""
	}
	name := wl.Spec.Template.Spec.Runtime.Class
	for _, c := range classes.Items {
		if c.Name == name || (name == "" && c.Spec.Default) {
			return c.Spec.Isolation.Boundary
		}
	}
	return ""
}

func narrowToGeneration(ctx context.Context, q querier, sel *selector, previous bool, w window, p *printer) error {
	index, which := 0, flagCurrent
	if previous {
		index, which = 1, flagPrevious
	}
	gens, err := discoverGenerations(ctx, q, *sel, w.start, w.end, index+1)
	if err != nil {
		return err
	}
	f, err := pickGeneration(gens, index)
	if err != nil {
		return err
	}
	if len(f.ids) == 0 {
		if previous {
			return errors.New("no earlier generation found in the window; widen it with --since")
		}
		return errors.New("no logs with a generation identity found in the window")
	}
	f.apply(sel)

	fmt.Fprintf(p.status, "Showing the %s generation of %d instance(s).\n", which, len(f.ids))
	if len(f.missing) > 0 {
		short := make([]string, len(f.missing))
		for i, m := range f.missing {
			short[i] = p.short(m)
		}
		fmt.Fprintf(p.status, "No %s generation found for: %s\n", which, strings.Join(short, ", "))
	}
	return nil
}

func albSelector(ctx context.Context, kube client.Client, workload string) (selector, error) {
	var sel selector
	proxies, err := proxiesFor(ctx, kube, workload)
	if err != nil {
		return sel, fmt.Errorf("looking up workload %q's URL: %w", workload, err)
	}
	if len(proxies) == 0 {
		return sel, fmt.Errorf("workload %q has no published URL, so it has no access logs — deploy with --http-port to publish one", workload)
	}
	sel.re(labelRouteName, albRoutePattern(proxies))
	return sel, nil
}

func emptyHint(workload string, alb bool, w window) string {
	msg := fmt.Sprintf("No log lines for workload %q in the %s window ending %s.",
		workload, w.end.Sub(w.start).Round(time.Minute), w.end.Format(time.RFC3339))
	if alb {
		return msg + "\nNo requests reached its URL; widen the window with --since."
	}
	return msg + "\nWiden it with --since, or check the instances started: datumctl compute instances --workload=" + workload
}
