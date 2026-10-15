package destroy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/url"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

// destroyPrompt states every consequence of the command, including the one a
// user is most likely to have forgotten: the workload's URL stops answering.
const destroyPrompt = "This will delete the workload, all its instances, and its URLs. Continue? (y/N): "

// leftoverPrompt is for the second run of a destroy whose first run deleted
// the workload but could not delete its URL.
const leftoverPrompt = "This will delete the URLs left behind by %s. Continue? (y/N): "

// summaryLabel keeps the summary block's values in one column.
const summaryLabel = 14

// leftoverBackendsDescription names what is left when a partial delete removed
// the URL and not the backends behind it. There is no hostname left to show,
// and the machinery is never named, so this is the plainest true thing to say.
const leftoverBackendsDescription = "URL backends from an unfinished destroy"

func Command() *cobra.Command {
	var yes bool

	cmd := &cobra.Command{
		Use:   "destroy <workload-name>",
		Short: "Delete a workload, all its instances, and its URLs",
		Long: `Delete a workload and everything that serves it: its instances and the URLs it
answers on.

Custom domains are not deleted. A verified domain is a project asset that
outlives any one workload.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDestroy(cmd, args, yes)
		},
		ValidArgsFunction: util.CompleteWorkloadNames,
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")

	return cmd
}

func runDestroy(cmd *cobra.Command, args []string, yes bool) error {
	project := util.ProjectFromCmd(cmd)

	c, err := util.NewClient(project)
	if err != nil {
		return err
	}

	return destroyWorkload(context.Background(), cmd.OutOrStdout(), cmd.ErrOrStderr(), c, project, args[0], yes)
}

// destroyWorkload deletes the workload and then the objects that put it on a
// URL. The client is a parameter so the whole flow is testable against a fake
// one.
func destroyWorkload(ctx context.Context, out, errOut io.Writer, c client.Client, project, workloadName string, yes bool) error {
	var workload computev1alpha.Workload
	err := c.Get(ctx, types.NamespacedName{Namespace: util.ResourceNamespace, Name: workloadName}, &workload)
	switch {
	case err == nil:
	case k8serrors.IsNotFound(err):
		// The workload is gone. Its URL may not be — a previous destroy can
		// have deleted the workload and failed on the URL, and this is the
		// command that run told the user to repeat.
		return destroyLeftoverURLs(ctx, out, errOut, c, project, workloadName, yes)
	default:
		return fmt.Errorf("getting workload: %w", err)
	}

	// The URL is read before anything is deleted: the summary has to be able
	// to name what will stop answering.
	info, urlErr := url.ForWorkload(ctx, c, workloadName)
	if urlErr != nil {
		fmt.Fprintf(errOut, "Warning: could not read URLs for %q: %v\n", workloadName, urlErr)
	}

	printSummary(out, &workload, info)

	confirmed, err := confirm(out, yes, destroyPrompt)
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(out, "Aborted.")
		return nil
	}

	if err := c.Delete(ctx, &workload); err != nil {
		return fmt.Errorf("deleting workload: %w", err)
	}
	fmt.Fprintf(out, "workload/%s deleted.\n", workloadName)

	// The URL objects are deleted explicitly rather than left to
	// owner-reference garbage collection, which a project control plane does
	// not guarantee. A failure here fails the command: the destroy was asked to
	// stop the URLs answering and it did not, and a script reading exit 0 would
	// carry on believing otherwise. The message still says the workload is
	// gone, because it is.
	if err := url.Unpublish(ctx, c, workloadName); err != nil {
		reportLeftoverURLs(errOut, workloadName, info)
		return err
	}

	return nil
}

// destroyLeftoverURLs handles a workload that is already gone. When it left no
// URL behind there is nothing to do and the workload really is missing; when
// it did, this cleans it up rather than making the user reach for the API.
func destroyLeftoverURLs(ctx context.Context, out, errOut io.Writer, c client.Client, project, workloadName string, yes bool) error {
	info, err := url.ForWorkload(ctx, c, workloadName)
	if err != nil {
		fmt.Fprintf(errOut, "Warning: could not read URLs for %q: %v\n", workloadName, err)
	}

	// A partial delete can remove the URL and leave its backends: the lookup
	// keys on the URL, so it reports nothing while there is still something
	// there. Asking for the backends directly is what makes the leftovers of
	// every partial delete reachable — without it the user is left holding
	// objects no command can remove.
	backends, backendsErr := leftoverBackends(ctx, c, workloadName)

	// Fail closed. Telling a user there is nothing to clean up is a claim, and a
	// read that failed is not evidence for it — the leftovers this command
	// exists to remove would be exactly what went unseen. Same choice as
	// deploy's existingHostnames, which fails closed rather than detaching
	// domains it could not read.
	if backendsErr != nil {
		return fmt.Errorf("checking for leftover URL resources of %q: %w", workloadName, backendsErr)
	}
	if err != nil && !backends {
		return fmt.Errorf("checking for leftover URLs of %q: %w", workloadName, err)
	}

	if info == nil && !backends {
		return fmt.Errorf("workload %q not found in project %s", workloadName, project)
	}

	// With the URL itself already gone there is no hostname left to name, so
	// the summary and the closing line both fall back to what does remain.
	urls := hostnameURLs(info)

	fmt.Fprintf(out, "%-*s %s (already deleted)\n", summaryLabel, "Workload:", workloadName)
	if len(urls) > 0 {
		printURLs(out, info)
	} else {
		fmt.Fprintf(out, "%-*s %s\n", summaryLabel, "Leftovers:", leftoverBackendsDescription)
	}
	fmt.Fprintln(out)

	confirmed, err := confirm(out, yes, fmt.Sprintf(leftoverPrompt, workloadName))
	if err != nil {
		return err
	}
	if !confirmed {
		fmt.Fprintln(out, "Aborted.")
		return nil
	}

	// Nothing else is being deleted here, so a failure is the command failing.
	if err := url.Unpublish(ctx, c, workloadName); err != nil {
		return err
	}

	if len(urls) > 0 {
		fmt.Fprintf(out, "URLs for %s deleted.\n", workloadName)
	} else {
		fmt.Fprintf(out, "Leftover URL backends for %s deleted.\n", workloadName)
	}
	return nil
}

// printSummary states what is about to be deleted, in the user's terms.
func printSummary(out io.Writer, workload *computev1alpha.Workload, info *url.Info) {
	var allLocations []string
	var totalMin int32
	for _, p := range workload.Spec.Placements {
		for _, ref := range p.Locations {
			allLocations = append(allLocations, ref.Name)
		}
		totalMin += p.ScaleSettings.MinReplicas
	}

	fmt.Fprintf(out, "%-*s %s\n", summaryLabel, "Workload:", workload.Name)
	fmt.Fprintf(out, "%-*s %d  Locations: %s\n", summaryLabel, "Placements:",
		len(workload.Spec.Placements), strings.Join(allLocations, ", "))
	fmt.Fprintf(out, "%-*s %d\n", summaryLabel, "Min replicas:", totalMin)
	printURLs(out, info)
	fmt.Fprintln(out)
}

// leftoverBackends reports whether the URL backends published for a workload
// are still in the project. It is asked only about a workload that is already
// gone, where anything still labelled with its name is debris from a destroy
// that did not finish.
//
// A control plane that does not serve the kind at all has nothing left over:
// that is an empty project, not a failure to report.
func leftoverBackends(ctx context.Context, c client.Client, workloadName string) (bool, error) {
	var services networkingv1alpha.NetworkServiceList
	err := c.List(ctx, &services,
		client.InNamespace(util.ResourceNamespace),
		client.MatchingLabels{computev1alpha.WorkloadNameLabel: workloadName})
	switch {
	case err == nil:
		return len(services.Items) > 0, nil
	case notServed(err):
		return false, nil
	default:
		return false, err
	}
}

// notServed reports whether a list error means the control plane does not
// serve this kind, rather than that the read failed.
func notServed(err error) bool {
	return k8serrors.IsNotFound(err) ||
		meta.IsNoMatchError(err) ||
		runtime.IsNotRegisteredError(err)
}

// printURLs adds the URLs line, when there is one. A workload with no HTTP
// port has no line at all rather than a line saying so: the summary lists what
// is being deleted.
func printURLs(out io.Writer, info *url.Info) {
	urls := hostnameURLs(info)
	if len(urls) == 0 {
		return
	}
	fmt.Fprintf(out, "%-*s %s\n", summaryLabel, "URLs:", strings.Join(urls, ", "))
}

// hostnameURLs lists every URL the workload answers on, custom hostnames
// first, the platform-managed one last, as the url package orders them.
func hostnameURLs(info *url.Info) []string {
	if info == nil {
		return nil
	}
	urls := make([]string, 0, len(info.Hostnames))
	for _, h := range info.Hostnames {
		urls = append(urls, h.URL)
	}
	if len(urls) == 0 && info.URL != "" {
		urls = append(urls, info.URL)
	}
	return urls
}

// reportLeftoverURLs explains a URL that outlived its workload. It names
// exactly what is still answering and the command that finishes the job,
// because a URL still serving traffic for a workload the user believes they
// deleted is the worst possible way to be quiet.
//
// The cause is not repeated here: the caller returns it, and it is printed as
// the command's error immediately after this block.
func reportLeftoverURLs(errOut io.Writer, workloadName string, info *url.Info) {
	fmt.Fprintf(errOut, "\nThe workload was deleted, but its URLs were not.\n")
	for _, u := range hostnameURLs(info) {
		fmt.Fprintf(errOut, "  %s may keep answering.\n", u)
	}
	fmt.Fprintf(errOut, "  Run 'datumctl compute destroy %s' again to remove them.\n", workloadName)
}

// confirm asks the question and reports whether the user said yes. --yes skips
// it; so does a non-interactive run, which is the behaviour this command has
// always had.
func confirm(out io.Writer, yes bool, question string) (bool, error) {
	if yes || !term.IsTerminal(int(os.Stdin.Fd())) {
		return true, nil
	}

	fmt.Fprint(out, question)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("reading confirmation: %w", err)
	}
	line = strings.TrimSpace(line)
	return line == "y" || line == "Y", nil
}
