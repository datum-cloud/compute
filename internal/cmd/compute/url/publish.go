// SPDX-License-Identifier: AGPL-3.0-only

package url

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/cmd/compute/util"
	networkingv1alpha "go.datum.net/network-services-operator/api/v1alpha"
)

const (
	// pollInterval matches the rollout watcher, so a deploy that publishes and
	// a deploy that only rolls out feel the same.
	pollInterval = 2 * time.Second

	// blockingGrace is how long to wait before repeating what the server says
	// is holding the URL up. Every object starts out reporting "waiting for
	// controller", and echoing that immediately is noise, not diagnosis.
	blockingGrace = 20 * time.Second

	// labelWidth aligns the progress labels ("Backends", "Edge",
	// "Certificate") in a fixed column.
	labelWidth = 12

	// maxReadFailures is how many consecutive failed reads the wait rides out
	// before giving up and reporting the last one.
	//
	// A read that fails once is a blip and the next tick is a better answer
	// than failing a deploy that is going fine. A read that fails every time is
	// something else — a missing list permission is the everyday one — and
	// polling it forever turns `deploy --http-port` into a silent hang. Three
	// ticks is a few seconds: long enough for a blip, short enough that a user
	// is told what is wrong rather than left watching a cursor.
	maxReadFailures = 3

	// maxWait bounds the whole wait, so no caller can hang forever even while
	// the control plane answers happily and simply never finishes. It is far
	// longer than publishing takes; reaching it means something is stuck.
	maxWait = 15 * time.Minute
)

// Publish creates or updates the two objects that put a workload on a URL and
// waits until that URL answers, printing progress as the platform reports it:
//
//	Backends     4 healthy across DFW, IAD
//	Edge         programmed
//	Certificate  issued
//
// It prints progress lines only. The caller prints whatever heading precedes
// them and the final URL — the URL is the deliverable and belongs to the
// command that was asked for it.
//
// The NetworkService is written before the HTTPProxy: a proxy naming a service
// that does not exist yet reports a missing backend, which the user would see
// as a spurious failure.
//
// hostnames are custom hostnames; pass nil for the managed URL alone.
//
// Cancelling ctx (Ctrl-C) detaches: publishing continues on the platform, a
// note says how to pick it up again, and Publish returns (nil, nil). A nil
// Info with a nil error means "still going, we stopped watching" — never an
// error, and never a reason for the caller to fail.
func Publish(
	ctx context.Context,
	out io.Writer,
	c client.Client,
	w *computev1alpha.Workload,
	portName string,
	port int32,
	hostnames []string,
) (*Info, error) {
	// A write interrupted by Ctrl-C is a detach like any other. Reporting the
	// cancelled context as a failure would exit 1 on a keystroke the user was
	// told is safe.
	if err := Declare(ctx, c, w, portName, port, hostnames); err != nil {
		if ctx.Err() != nil {
			detached(out, w.Name)
			return nil, nil
		}
		return nil, err
	}

	return Wait(ctx, out, c, w.Name)
}

// Declare writes the two objects that put a workload on a URL and returns
// without waiting for that URL to answer.
//
// It is the half of Publish a caller wants when the URL has to be declared
// early — a deploy declares it alongside the workload so that backends
// register as instances come up, then waits only once the rollout is done.
// It prints nothing: at the point it runs there is nothing to report yet.
//
// The NetworkService is written before the HTTPProxy: a proxy naming a service
// that does not exist yet reports a missing backend, which the user would see
// as a spurious failure.
//
// hostnames are custom hostnames; pass nil for the managed URL alone.
func Declare(
	ctx context.Context,
	c client.Client,
	w *computev1alpha.Workload,
	portName string,
	port int32,
	hostnames []string,
) error {
	if err := applyService(ctx, c, BuildNetworkService(w, portName, port)); err != nil {
		return err
	}
	return applyProxy(ctx, c, BuildHTTPProxy(w, portName, hostnames))
}

// detached prints the note that says publishing carries on without us, and how
// to pick it back up.
func detached(out io.Writer, workloadName string) {
	fmt.Fprintf(out, "\nDetached. Publishing continues in the background.\n")
	fmt.Fprintf(out, "  Check it with: datumctl compute workloads describe %s\n", workloadName)
}

// Wait polls until the workload's URL is live, printing each stage as it
// lands. It is the half of Publish that a caller which already called Declare
// still needs.
//
// It always terminates: Ctrl-C detaches, a control plane that cannot be read
// gives up after maxReadFailures and returns what it was told, and the whole
// wait ends at maxWait however healthy the polling looks.
//
// Cancelling ctx (Ctrl-C) detaches: a note says how to pick the URL up again
// and Wait returns (nil, nil), which is never an error.
func Wait(ctx context.Context, out io.Writer, c client.Client, workloadName string) (*Info, error) {
	p := &progress{out: out, started: time.Now(), seen: map[string]bool{}}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()

	var failures int
	for {
		info, done, err := p.check(ctx, c, workloadName)
		switch {
		case done:
			// A URL that came up on the same tick the user interrupted is
			// still a URL, and worth more to them than a detach note.
			return info, nil

		case ctx.Err() != nil:
			// Otherwise detaching wins over whatever the last read said: a
			// read that was cancelled failed because the user asked it to.
			detached(out, workloadName)
			return nil, nil

		case err != nil:
			failures++
			if failures >= maxReadFailures {
				return nil, fmt.Errorf("checking the URL for %q: %w", workloadName, err)
			}

		default:
			failures = 0
		}

		select {
		case <-ctx.Done():
			detached(out, workloadName)
			return nil, nil

		case <-deadline.C:
			return nil, fmt.Errorf(
				"the URL for %q was still not answering after %s — it may yet come up; check it with: datumctl compute workloads describe %s",
				workloadName, maxWait, workloadName)

		case <-ticker.C:
		}
	}
}

// progress prints each stage of publishing exactly once, and repeats nothing.
type progress struct {
	out     io.Writer
	started time.Time
	seen    map[string]bool
}

// check reads current state and reports whether the URL is live, along with
// whatever went wrong reading it. The caller decides how much failure to ride
// out; a single failure means nothing, since the objects were written a moment
// ago and the next tick is a better answer than failing a deploy that is going
// fine.
//
// A workload that is simply not published yet is not a failure: there is
// nothing to report and nothing to give up over.
func (p *progress) check(ctx context.Context, c client.Client, workloadName string) (*Info, bool, error) {
	info, err := ForWorkload(ctx, c, workloadName)
	if err != nil {
		return nil, false, err
	}
	if info == nil {
		return nil, false, nil
	}

	if info.Backends.Healthy > 0 {
		p.line("Backends", fmt.Sprintf("%d healthy across %s", info.Backends.Healthy, strings.Join(servingCities(info), ", ")))
	}
	if info.EdgeProgrammed {
		p.line("Edge", "programmed")
	}
	if info.CertificateIssued {
		p.line("Certificate", "issued")
	}

	if info.Live() {
		return info, true, nil
	}

	if time.Since(p.started) > blockingGrace {
		p.blocking(info)
	}
	return nil, false, nil
}

// line prints one progress row, skipping rows already printed with the same
// value. A count that changes as instances register is worth reprinting; a
// repeat of the same fact is not.
func (p *progress) line(label, value string) {
	key := label + "\x00" + value
	if p.seen[key] {
		return
	}
	p.seen[key] = true
	fmt.Fprintf(p.out, "  %-*s %s\n", labelWidth, label, value)
}

// blocking echoes, verbatim and once each, whatever the server says is holding
// the URL up. The CLI never interprets a reason: a condition the CLI has never
// heard of shows up here without a release.
func (p *progress) blocking(info *Info) {
	p.blockingFrom(info.ServiceConditions, networkingv1alpha.NetworkServiceReady)
	p.blockingFrom(info.ProxyConditions, networkingv1alpha.HTTPProxyConditionProgrammed)
	for _, h := range info.Hostnames {
		if !h.Active && h.Detail != "" {
			p.note(fmt.Sprintf("%s: %s", h.Hostname, h.Detail))
		}
	}
}

func (p *progress) blockingFrom(conditions []metav1.Condition, condType string) {
	reason, message, blocked := util.ReadinessBlock(conditions, condType)
	if !blocked || reason == "" {
		return
	}
	p.note(HumanBlock(reason, message))
}

func (p *progress) note(text string) {
	if p.seen[text] {
		return
	}
	p.seen[text] = true
	fmt.Fprintf(p.out, "  %-*s %s\n", labelWidth, "", text)
}

// servingCities names the cities taking traffic, for the backends line. It
// falls back to every city with members so the line is never empty while the
// platform is still deciding what is in rotation.
func servingCities(info *Info) []string {
	serving := make([]string, 0, len(info.Locations))
	all := make([]string, 0, len(info.Locations))
	for _, l := range info.Locations {
		all = append(all, l.Location)
		if l.Serving {
			serving = append(serving, l.Location)
		}
	}
	if len(serving) > 0 {
		return serving
	}
	return all
}

// applyService creates the NetworkService, or brings an existing one in line
// with what the workload now declares.
func applyService(ctx context.Context, c client.Client, desired *networkingv1alpha.NetworkService) error {
	var existing networkingv1alpha.NetworkService
	err := c.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if k8serrors.IsNotFound(err) {
		if err := c.Create(ctx, desired); err != nil {
			return fmt.Errorf("publishing backends for %q: %w", desired.Name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading published backends for %q: %w", desired.Name, err)
	}

	if reflect.DeepEqual(existing.Spec, desired.Spec) && metaCurrent(&existing, desired) {
		return nil
	}
	existing.Spec = desired.Spec
	adoptMeta(&existing, desired)
	if err := c.Update(ctx, &existing); err != nil {
		return fmt.Errorf("updating published backends for %q: %w", desired.Name, err)
	}
	return nil
}

// applyProxy creates the HTTPProxy, or brings an existing one in line with
// what the workload now declares.
func applyProxy(ctx context.Context, c client.Client, desired *networkingv1alpha.HTTPProxy) error {
	var existing networkingv1alpha.HTTPProxy
	err := c.Get(ctx, client.ObjectKeyFromObject(desired), &existing)
	if k8serrors.IsNotFound(err) {
		if err := c.Create(ctx, desired); err != nil {
			return fmt.Errorf("publishing URL for %q: %w", desired.Name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("reading published URL for %q: %w", desired.Name, err)
	}

	if reflect.DeepEqual(existing.Spec, desired.Spec) && metaCurrent(&existing, desired) {
		return nil
	}
	existing.Spec = desired.Spec
	adoptMeta(&existing, desired)
	if err := c.Update(ctx, &existing); err != nil {
		return fmt.Errorf("updating published URL for %q: %w", desired.Name, err)
	}
	return nil
}

// metaCurrent reports whether an existing object already carries the labels
// and owner reference the desired object declares.
func metaCurrent(existing, desired client.Object) bool {
	for k, v := range desired.GetLabels() {
		if existing.GetLabels()[k] != v {
			return false
		}
	}
	return hasOwner(existing, desired)
}

// hasOwner reports whether existing already references the desired owner.
func hasOwner(existing, desired client.Object) bool {
	owners := desired.GetOwnerReferences()
	if len(owners) == 0 {
		return true
	}
	for _, o := range existing.GetOwnerReferences() {
		if o.UID == owners[0].UID && o.Kind == owners[0].Kind {
			return true
		}
	}
	return false
}

// adoptMeta merges the labels and owner reference onto an object that already
// exists, without dropping anything a user put there.
func adoptMeta(existing, desired client.Object) {
	labels := existing.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	for k, v := range desired.GetLabels() {
		labels[k] = v
	}
	existing.SetLabels(labels)

	if !hasOwner(existing, desired) {
		existing.SetOwnerReferences(append(existing.GetOwnerReferences(), desired.GetOwnerReferences()...))
	}
}

// Unpublish removes a workload's URL: the HTTPProxy first, then the
// NetworkService behind it. Deleting the service first would leave the proxy
// reporting a missing backend for as long as the delete takes.
//
// Each object is read by name and deleted only if it is there, never deleted
// by selector. A selector delete is the deletecollection permission, which
// project roles do not grant: it failed every destroy, including a destroy
// with no URL to remove. There is exactly one of each object per workload and
// it carries the workload's name, so the name is all a read ever needs here.
//
// Objects that are not there are not an error — unpublishing something that
// was never published is a no-op, which is what `destroy` needs.
func Unpublish(ctx context.Context, c client.Client, workloadName string) error {
	key := client.ObjectKey{Namespace: util.ResourceNamespace, Name: ResourceName(workloadName)}

	if err := deleteIfFound(ctx, c, key, &networkingv1alpha.HTTPProxy{}); err != nil {
		return fmt.Errorf("removing URL for %q: %w", workloadName, err)
	}
	if err := deleteIfFound(ctx, c, key, &networkingv1alpha.NetworkService{}); err != nil {
		return fmt.Errorf("removing URL backends for %q: %w", workloadName, err)
	}

	return nil
}

// deleteIfFound deletes an object only once a read has found it, so nothing is
// asked of the server for a workload that published no URL. An object that
// disappears between the read and the delete is already what was wanted.
func deleteIfFound(ctx context.Context, c client.Client, key client.ObjectKey, obj client.Object) error {
	if err := c.Get(ctx, key, obj); err != nil {
		if notPublished(err) {
			return nil
		}
		return err
	}
	if err := c.Delete(ctx, obj); err != nil && !notPublished(err) {
		return err
	}
	return nil
}
