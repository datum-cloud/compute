package util

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	servicesv1alpha1 "go.miloapis.com/service-catalog/api/v1alpha1"
	"go.datum.net/datumctl/plugin"
	"golang.org/x/term"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const computeServiceName = "compute"

// EnsureComputeEntitlement checks that the selected project has an active
// ServiceEntitlement for the compute service. If none exists, it prompts the
// user (via in/out) to request access. out should be cmd.ErrOrStderr() so the
// prompt does not pollute structured output.
func EnsureComputeEntitlement(ctx context.Context, project string, in io.Reader, out io.Writer) error {
	if project == "" {
		return nil
	}

	wc, err := newEntitlementClient(project)
	if err != nil {
		return err
	}

	var list servicesv1alpha1.ServiceEntitlementList
	if err := wc.List(ctx, &list); err != nil {
		if apimeta.IsNoMatchError(err) {
			// API not installed in this project's VCP — treat as no entitlement.
			return promptAndRequestAccess(ctx, project, wc, in, out)
		}
		return fmt.Errorf("checking service entitlement: %w", err)
	}

	for i := range list.Items {
		item := &list.Items[i]
		if item.Spec.ServiceRef.Name != computeServiceName {
			continue
		}
		switch item.Status.Phase {
		case servicesv1alpha1.EntitlementPhaseActive:
			return nil
		case servicesv1alpha1.EntitlementPhasePendingApproval:
			return fmt.Errorf(
				"compute service entitlement for project %q is pending approval\n\n"+
					"Check status with: datumctl services list",
				project,
			)
		case servicesv1alpha1.EntitlementPhaseRejected:
			return fmt.Errorf(
				"compute service entitlement for project %q was rejected\n\n"+
					"Re-enable with: datumctl services enable %s",
				project, computeServiceName,
			)
		}
	}

	return promptAndRequestAccess(ctx, project, wc, in, out)
}

func promptAndRequestAccess(ctx context.Context, project string, wc client.WithWatch, in io.Reader, out io.Writer) error {
	if !isTTY(in) {
		return fmt.Errorf(
			"compute service is not enabled for project %q\n\n"+
				"Enable it with: datumctl services enable %s",
			project, computeServiceName,
		)
	}

	fmt.Fprintf(out, "Compute is not enabled for project %q.\n", project)
	fmt.Fprintf(out, "Would you like to request access? [y/N]: ")

	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		return fmt.Errorf("compute service is not enabled for project %q", project)
	}
	answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf(
			"compute service is not enabled for project %q\n\n"+
				"Enable it with: datumctl services enable %s",
			project, computeServiceName,
		)
	}

	fmt.Fprintf(out, "Requesting access to compute for project %q...\n", project)

	entitlement := &servicesv1alpha1.ServiceEntitlement{
		ObjectMeta: metav1.ObjectMeta{Name: computeServiceName},
		Spec: servicesv1alpha1.ServiceEntitlementSpec{
			ServiceRef: servicesv1alpha1.ServiceRef{Name: computeServiceName},
		},
	}
	if err := wc.Create(ctx, entitlement); err != nil {
		return fmt.Errorf("requesting compute access: %w", err)
	}

	// Watch for the Ready condition to appear (set by the reconciler asynchronously).
	watchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	watcher, err := wc.Watch(watchCtx, &servicesv1alpha1.ServiceEntitlementList{})
	if err != nil {
		return fmt.Errorf("watching entitlement status: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-watchCtx.Done():
			fmt.Fprintf(out, "\nAccess to compute for project %q has been requested.\n", project)
			fmt.Fprintf(out, "Run your command again once it becomes active.\n\n")
			fmt.Fprintf(out, "Check status with: datumctl services list\n")
			return fmt.Errorf("compute access is not yet active — try again in a moment")

		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed unexpectedly")
			}
			if event.Type != watch.Modified && event.Type != watch.Added {
				continue
			}
			item, ok := event.Object.(*servicesv1alpha1.ServiceEntitlement)
			if !ok || item.Spec.ServiceRef.Name != computeServiceName {
				continue
			}
			if apimeta.FindStatusCondition(item.Status.Conditions, "Ready") == nil {
				continue
			}
			switch item.Status.Phase {
			case servicesv1alpha1.EntitlementPhaseActive:
				fmt.Fprintf(out, "Compute enabled for project %q.\n\n", project)
				return nil
			case servicesv1alpha1.EntitlementPhasePendingApproval:
				fmt.Fprintf(out, "\nYour request to enable compute for project %q has been submitted,\n", project)
				fmt.Fprintf(out, "but it requires approval before you can use the service.\n")
				fmt.Fprintf(out, "You will be notified when access is granted.\n\n")
				fmt.Fprintf(out, "Check status with: datumctl services list\n")
				return fmt.Errorf("compute access is pending approval")
			default:
				return fmt.Errorf("compute entitlement for project %q entered unexpected phase %q", project, item.Status.Phase)
			}
		}
	}
}

func newEntitlementClient(project string) (client.WithWatch, error) {
	pluginCtx := plugin.Context()
	if pluginCtx.APIHost == "" {
		return nil, fmt.Errorf("DATUM_API_HOST is not set; is this plugin running via datumctl?")
	}

	token, err := plugin.Token()
	if err != nil {
		return nil, fmt.Errorf("getting credentials: %w", err)
	}

	scheme := runtime.NewScheme()
	if err := servicesv1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("registering services scheme: %w", err)
	}

	cfg := &rest.Config{
		Host:        ProjectControlPlaneURL(pluginCtx.APIHost, project),
		BearerToken: token,
	}

	return client.NewWithWatch(cfg, client.Options{Scheme: scheme})
}

func isTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
