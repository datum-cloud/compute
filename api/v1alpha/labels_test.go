// SPDX-License-Identifier: AGPL-3.0-only

package v1alpha

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestNetworkHostLabelValue(t *testing.T) {
	long := "node-" + strings.Repeat("a", 80) + ".example.com"
	longSibling := "node-" + strings.Repeat("a", 80) + ".example.org"
	cutOnDot := strings.Repeat("a", 45) + "." + strings.Repeat("b", 30)
	allSeparators := strings.Repeat(".", 50) + strings.Repeat("a", 20)

	tests := []struct {
		name     string
		nodeName string
		want     string
	}{
		{name: "short name is used as is", nodeName: "node-a", want: "node-a"},
		{name: "63 character name is used as is", nodeName: strings.Repeat("a", 63), want: strings.Repeat("a", 63)},
		{name: "dotted name within the limit is used as is", nodeName: "node-a.example.com", want: "node-a.example.com"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NetworkHostLabelValue(tt.nodeName); got != tt.want {
				t.Fatalf("NetworkHostLabelValue(%q) = %q, want %q", tt.nodeName, got, tt.want)
			}
		})
	}

	for _, nodeName := range []string{long, longSibling, cutOnDot, strings.Repeat("a", 64)} {
		value := NetworkHostLabelValue(nodeName)
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			t.Errorf("NetworkHostLabelValue(%q) = %q is not a valid label value: %v", nodeName, value, errs)
		}
		if !strings.HasPrefix(value, nodeName[:10]) {
			t.Errorf("NetworkHostLabelValue(%q) = %q does not keep the name's leading characters", nodeName, value)
		}
		if again := NetworkHostLabelValue(nodeName); again != value {
			t.Errorf("NetworkHostLabelValue(%q) is not stable: %q then %q", nodeName, value, again)
		}
	}

	if got := NetworkHostLabelValue(cutOnDot); !strings.HasPrefix(got, strings.Repeat("a", 45)+"-") {
		t.Errorf("NetworkHostLabelValue(%q) = %q, want the trailing separator trimmed before the hash", cutOnDot, got)
	}

	for _, nodeName := range []string{allSeparators, "host:a", "-host-a"} {
		value := NetworkHostLabelValue(nodeName)
		if errs := validation.IsValidLabelValue(value); len(errs) > 0 {
			t.Errorf("NetworkHostLabelValue(%q) = %q is not a valid label value: %v", nodeName, value, errs)
		}
		if len(value) != networkHostHashLength {
			t.Errorf("NetworkHostLabelValue(%q) = %q, want the hash alone", nodeName, value)
		}
	}

	if NetworkHostLabelValue(long) == NetworkHostLabelValue(longSibling) {
		t.Errorf("long names sharing a prefix map to the same value %q", NetworkHostLabelValue(long))
	}
}

func TestNetworkHostSelector(t *testing.T) {
	for _, nodeName := range []string{"node-a", "node-" + strings.Repeat("a", 80)} {
		selector := NetworkHostSelector(nodeName)
		own := labels.Set{NetworkHostLabel: NetworkHostLabelValue(nodeName)}
		other := labels.Set{NetworkHostLabel: NetworkHostLabelValue(nodeName + "-other")}
		if !selector.Matches(own) {
			t.Errorf("NetworkHostSelector(%q) does not match the host's own Pods", nodeName)
		}
		if selector.Matches(other) {
			t.Errorf("NetworkHostSelector(%q) matches another host's Pods", nodeName)
		}
		if selector.Matches(labels.Set{}) {
			t.Errorf("NetworkHostSelector(%q) matches a Pod with no host", nodeName)
		}
	}

	if NetworkHostSelector("").Matches(labels.Set{NetworkHostLabel: ""}) {
		t.Error(`NetworkHostSelector("") matches Pods, want it to select nothing`)
	}
}
