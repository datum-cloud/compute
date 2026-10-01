// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/pkg/browser"

	"go.datum.net/datumctl/plugin"
)

// portalSlug is the compute service's page in the cloud portal, which the
// portal derives from the service name, compute.datumapis.com.
const portalSlug = "compute-datumapis-com"

func openPortal(status io.Writer, project, workload string, instances []string) error {
	if project == "" {
		return errors.New("no project set — pass --project or run 'datumctl config set project <name>'")
	}
	instance := ""
	if len(instances) == 1 {
		instance = qualify(workload, instances)[0]
	}
	u, err := portalLogsURL(plugin.Context().APIHost, project, workload, instance)
	if err != nil {
		return err
	}
	// The opener's own output would only clutter the terminal.
	browser.Stdout, browser.Stderr = io.Discard, io.Discard
	if err := browser.OpenURL(u); err != nil {
		fmt.Fprintf(status, "Couldn't open a browser. Open %s\n", u)
		return nil
	}
	fmt.Fprintf(status, "Opened %s\n", u)
	return nil
}

// portalLogsURL returns the portal's Logs tab for a workload, or for one
// instance of it.
func portalLogsURL(apiHost, project, workload, instance string) (string, error) {
	base, err := portalBase(apiHost)
	if err != nil {
		return "", err
	}
	u := base + "/project/" + url.PathEscape(project) + "/services/" + portalSlug + "/" + url.PathEscape(workload)
	if instance != "" {
		u += "/instances/" + url.PathEscape(instance)
	}
	return u + "/logs", nil
}

// portalBase mirrors datumctl's mapping from API host to portal.
func portalBase(apiHost string) (string, error) {
	switch {
	case strings.HasSuffix(apiHost, ".staging.env.datum.net"):
		return "https://cloud.staging.env.datum.net", nil
	case strings.HasSuffix(apiHost, ".datum.net"):
		return "https://cloud.datum.net", nil
	}
	return "", fmt.Errorf("no cloud portal is known for API host %q", apiHost)
}
