package util

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"go.datum.net/datumctl/plugin"
	"golang.org/x/term"
)

const computeServiceName = "compute"

type entitlementList struct {
	Items []entitlementItem `json:"items"`
}

type entitlementItem struct {
	Spec struct {
		ServiceRef struct {
			Name string `json:"name"`
		} `json:"serviceRef"`
	} `json:"spec"`
	Status struct {
		Phase string `json:"phase"`
	} `json:"status"`
}

// EnsureComputeEntitlement checks that the selected project has an active
// ServiceEntitlement for the compute service. If none exists, it prompts the
// user (via in/out) to request access. out should be cmd.ErrOrStderr() so the
// prompt does not pollute structured output.
func EnsureComputeEntitlement(ctx context.Context, project string, in io.Reader, out io.Writer) error {
	if project == "" {
		// NewClient will surface the missing-project error.
		return nil
	}

	pluginCtx := plugin.Context()
	if pluginCtx.APIHost == "" {
		return fmt.Errorf("DATUM_API_HOST is not set; is this plugin running via datumctl?")
	}

	token, err := plugin.Token()
	if err != nil {
		return fmt.Errorf("getting credentials: %w", err)
	}

	baseURL := ProjectControlPlaneURL(pluginCtx.APIHost, project)
	list, err := listEntitlements(ctx, baseURL, token)
	if err != nil {
		return err
	}

	for _, item := range list.Items {
		if item.Spec.ServiceRef.Name != computeServiceName {
			continue
		}
		switch item.Status.Phase {
		case "Active":
			return nil
		case "PendingApproval":
			return fmt.Errorf(
				"compute service entitlement for project %q is pending approval\n\n"+
					"Check status with: datumctl services list",
				project,
			)
		case "Rejected":
			return fmt.Errorf(
				"compute service entitlement for project %q was rejected\n\n"+
					"Re-enable with: datumctl services enable %s",
				project, computeServiceName,
			)
		}
	}

	// No entitlement found — prompt the user.
	return promptAndRequestAccess(ctx, project, baseURL, token, in, out)
}

func listEntitlements(ctx context.Context, baseURL, token string) (*entitlementList, error) {
	url := baseURL + "/apis/services.miloapis.com/v1alpha1/serviceentitlements"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("building entitlement request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("checking service entitlement: %w", err)
	}
	defer resp.Body.Close()

	// 404 means the service-catalog API isn't installed in this project's VCP,
	// which is equivalent to having no entitlement — fall through to the prompt.
	if resp.StatusCode == http.StatusNotFound {
		return &entitlementList{}, nil
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"checking service entitlement: unexpected status %d",
			resp.StatusCode,
		)
	}

	var list entitlementList
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decoding service entitlements: %w", err)
	}
	return &list, nil
}

func promptAndRequestAccess(ctx context.Context, project, baseURL, token string, in io.Reader, out io.Writer) error {
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
	if err := createEntitlement(ctx, baseURL, token); err != nil {
		return err
	}

	// Re-fetch to determine the resulting phase.
	list, err := listEntitlements(ctx, baseURL, token)
	if err != nil {
		return err
	}
	for _, item := range list.Items {
		if item.Spec.ServiceRef.Name != computeServiceName {
			continue
		}
		switch item.Status.Phase {
		case "Active":
			fmt.Fprintf(out, "Compute enabled for project %q.\n", project)
			return nil
		case "PendingApproval":
			fmt.Fprintf(out, "\nYour request to enable compute for project %q has been submitted,\n", project)
			fmt.Fprintf(out, "but it requires approval before you can use the service.\n")
			fmt.Fprintf(out, "You will be notified when access is granted.\n\n")
			fmt.Fprintf(out, "Check status with: datumctl services list\n")
			return fmt.Errorf("compute access is pending approval")
		}
	}

	return fmt.Errorf("entitlement created but status is not yet available — check: datumctl services list")
}

func createEntitlement(ctx context.Context, baseURL, token string) error {
	body := map[string]any{
		"apiVersion": "services.miloapis.com/v1alpha1",
		"kind":       "ServiceEntitlement",
		"metadata": map[string]any{
			"name": computeServiceName,
		},
		"spec": map[string]any{
			"serviceRef": map[string]any{
				"name": computeServiceName,
			},
		},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encoding entitlement request: %w", err)
	}

	url := baseURL + "/apis/services.miloapis.com/v1alpha1/serviceentitlements"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("building create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("requesting compute access: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("requesting compute access: unexpected status %d", resp.StatusCode)
	}
	return nil
}

func isTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}
