// SPDX-License-Identifier: AGPL-3.0-only

package deploy

import (
	"strings"
	"testing"
)

const (
	testCity             = "DFW"
	testLocation         = "us-east-1"
	errMutuallyExclusive = "mutually exclusive"
)

// TestResolveLocationSelector pins the three mutually exclusive ways a deploy
// says where to run. This validation was lifted out of deployFromFlags to keep
// it under the complexity limit, so it needs its own coverage: nothing else
// exercises it without a live control plane behind the activation gate.
func TestResolveLocationSelector(t *testing.T) {
	for _, tc := range []struct {
		name        string
		opts        options
		wantErr     string
		wantNil     bool
		wantMatches map[string]string
	}{{
		name:    "no placement flag at all",
		opts:    options{},
		wantErr: "--location is required",
	}, {
		name:    "location and city together",
		opts:    options{locations: []string{testLocation}, cities: []string{testCity}},
		wantErr: errMutuallyExclusive,
	}, {
		name:    "location and selector together",
		opts:    options{locations: []string{testLocation}, locationSelector: "a=b"},
		wantErr: errMutuallyExclusive,
	}, {
		name:    "all three together",
		opts:    options{locations: []string{testLocation}, cities: []string{testCity}, locationSelector: "a=b"},
		wantErr: errMutuallyExclusive,
	}, {
		name:    "named locations need no selector",
		opts:    options{locations: []string{testLocation, "eu-west-1"}},
		wantNil: true,
	}, {
		name:        "cities become a city-code selector",
		opts:        options{cities: []string{testCity}},
		wantMatches: map[string]string{"topology.datum.net/city-code": testCity},
	}, {
		name:        "an explicit selector is parsed",
		opts:        options{locationSelector: "topology.datum.net/region=us-east-1"},
		wantMatches: map[string]string{"topology.datum.net/region": testLocation},
	}, {
		name:    "an unparseable selector is reported, not ignored",
		opts:    options{locationSelector: "=="},
		wantErr: "invalid --location-selector",
	}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveLocationSelector(&tc.opts)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("want an error containing %q, got selector %v", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %q, want it to contain %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.wantNil {
				if got != nil {
					t.Errorf("selector = %v, want nil — named locations select nothing", got)
				}
				return
			}

			if got == nil {
				t.Fatal("selector is nil, want one")
			}
			for k, v := range tc.wantMatches {
				if got.MatchLabels[k] != v {
					t.Errorf("matchLabels[%q] = %q, want %q (got %v)", k, got.MatchLabels[k], v, got.MatchLabels)
				}
			}
		})
	}
}
