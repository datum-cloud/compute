// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	logsapi "go.miloapis.com/telemetry/cli/logs"
)

func TestWithHint(t *testing.T) {
	apiErr := func(code int) error {
		return fmt.Errorf("finding instance generations: %w",
			&logsapi.APIError{StatusCode: code, Status: fmt.Sprintf("%d %s", code, http.StatusText(code)), Detail: "denied"})
	}
	tests := []struct {
		err  error
		want string
	}{
		{apiErr(http.StatusForbidden), "finding instance generations: logs query returned 403 Forbidden: denied — reading logs requires the o11y.miloapis.com logs.query permission on this project"},
		{apiErr(http.StatusUnauthorized), "finding instance generations: logs query returned 401 Unauthorized: denied — run 'datumctl login' to refresh your credentials"},
		{apiErr(http.StatusBadRequest), "finding instance generations: logs query returned 400 Bad Request: denied"},
		{apiErr(http.StatusBadGateway), "finding instance generations: logs query returned 502 Bad Gateway: denied"},
		{errors.New("other"), "other"},
	}
	for _, tt := range tests {
		got := withHint(tt.err)
		if got.Error() != tt.want {
			t.Errorf("withHint() = %q, want %q", got, tt.want)
		}
		if !errors.Is(got, tt.err) {
			t.Errorf("withHint(%v) doesn't wrap the original error", tt.err)
		}
	}
}
