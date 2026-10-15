// SPDX-License-Identifier: AGPL-3.0-only

package url

import "testing"

// TestHumanBlockNeverShowsAReason pins product principle 4: a developer sees
// what the server wrote for them, never the identifier it filed it under.
func TestHumanBlockNeverShowsAReason(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reason  string
		message string
		want    string
	}{{
		name:    "the message wins whenever there is one",
		reason:  "NoMatchingInterfaces",
		message: "selector matched no network interface",
		want:    "selector matched no network interface",
	}, {
		name:   "a message-less reason is spaced out, not dropped",
		reason: "NoMatchingInterfaces",
		want:   "no matching interfaces",
	}, {
		name:   "a reason the CLI has never heard of still reads as words",
		reason: "SomeReasonTheCLIHasNeverHeardOf",
		want:   "some reason the CLI has never heard of",
	}, {
		name:   "acronyms survive",
		reason: "CertificateCARequired",
		want:   "certificate CA required",
	}, {
		name:   "a single word",
		reason: "Pending",
		want:   "pending",
	}, {
		name: "nothing at all still says something",
		want: statusPending,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HumanBlock(tc.reason, tc.message); got != tc.want {
				t.Errorf("HumanBlock(%q, %q) = %q, want %q", tc.reason, tc.message, got, tc.want)
			}
		})
	}
}
