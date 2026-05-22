package util

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RelativeAge returns a compact age string for table cells (no "ago" suffix).
//
//	< 60s  → "Xs"
//	< 60m  → "Xm"
//	< 24h  → "Xh"
//	>= 24h → "Xd"
func RelativeAge(t metav1.Time) string {
	d := time.Since(t.Time)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// RelativeAgeVerbose returns an age string with "ago" suffix for detail views.
func RelativeAgeVerbose(t metav1.Time) string {
	return RelativeAge(t) + " ago"
}
