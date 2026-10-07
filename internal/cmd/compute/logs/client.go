// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"go.datum.net/datumctl/plugin"
	logsapi "go.miloapis.com/telemetry/cli/logs"
)

type querier interface {
	logsapi.Querier
	LabelValues(ctx context.Context, label string, start, end time.Time) ([]string, error)
}

func newLogsClient(project string) (*logsapi.Client, error) {
	apiHost := plugin.Context().APIHost
	if apiHost == "" {
		return nil, errors.New("DATUM_API_HOST is not set; is this plugin running via datumctl?")
	}
	return logsapi.New(logsapi.ProjectURL(apiHost, project), logsapi.WithToken(plugin.Token)), nil
}

// withHint appends what the user can do about an API error. The 400 hint is
// about hand-written LogQL, which compute users never write.
func withHint(err error) error {
	var ae *logsapi.APIError
	if !errors.As(err, &ae) || ae.StatusCode == http.StatusBadRequest || ae.Hint() == "" {
		return err
	}
	return fmt.Errorf("%w — %s", err, ae.Hint())
}
