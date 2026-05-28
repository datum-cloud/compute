package util

import (
	"context"
	"strings"

	quotav1alpha1 "go.miloapis.com/milo/pkg/apis/quota/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// QuotaRow holds display-ready quota data for one resource type.
type QuotaRow struct {
	ResourceType string `json:"resourceType"`
	DisplayName  string `json:"displayName"`
	Unit         string `json:"unit"`
	Limit        int64  `json:"limit"`
	Used         int64  `json:"used"`
	Available    int64  `json:"available"`
}

// QuotaMeta overrides display metadata for a resource type. When provided,
// DisplayName, Unit, and Divisor take precedence over ResourceRegistration values.
type QuotaMeta struct {
	DisplayName string
	Unit        string
	// Divisor converts the stored integer value to display units (e.g. 1000 for
	// millicores → vCPUs). Zero is treated as 1.
	Divisor int64
	// Order controls the position of this row in the returned slice (ascending).
	// Rows without a meta entry sort after all meta rows, alphabetically.
	Order int
}

// ListServiceQuota returns quota rows for AllowanceBuckets whose resource type
// begins with resourceTypePrefix (e.g. "compute.datumapis.com"). projectClient
// must target the project's virtual control plane; platformClient must target
// the platform API server (used to fetch ResourceRegistrations for display
// metadata when no override is provided in meta).
//
// meta may be nil. When an entry exists for a resource type, its DisplayName,
// Unit, and Divisor are used; otherwise the ResourceRegistration's displayUnit
// field is used and the divisor defaults to 1.
func ListServiceQuota(
	ctx context.Context,
	projectClient, platformClient client.Client,
	resourceTypePrefix string,
	meta map[string]QuotaMeta,
	orderedTypes []string, // explicit display order; types not in this list follow alphabetically
) ([]QuotaRow, error) {
	// Fetch AllowanceBuckets from the project VCP.
	var bucketList quotav1alpha1.AllowanceBucketList
	if err := projectClient.List(ctx, &bucketList,
		client.InNamespace("milo-system"),
		client.MatchingLabels{"quota.miloapis.com/consumer-kind": "Project"},
	); err != nil {
		return nil, err
	}

	// Index buckets by resource type, filtering to the requested prefix.
	bucketByType := make(map[string]*quotav1alpha1.AllowanceBucket)
	for i := range bucketList.Items {
		b := &bucketList.Items[i]
		if strings.HasPrefix(b.Spec.ResourceType, resourceTypePrefix) {
			bucketByType[b.Spec.ResourceType] = b
		}
	}

	if len(bucketByType) == 0 {
		return nil, nil
	}

	// Fetch ResourceRegistrations from the platform for display metadata fallback.
	rrByType := make(map[string]*quotav1alpha1.ResourceRegistration)
	if platformClient != nil {
		var rrList quotav1alpha1.ResourceRegistrationList
		if err := platformClient.List(ctx, &rrList); err == nil {
			for i := range rrList.Items {
				rr := &rrList.Items[i]
				if strings.HasPrefix(rr.Spec.ResourceType, resourceTypePrefix) {
					rrByType[rr.Spec.ResourceType] = rr
				}
			}
		}
	}

	// Build an ordered index: position in orderedTypes slice.
	orderIndex := make(map[string]int, len(orderedTypes))
	for i, rt := range orderedTypes {
		orderIndex[rt] = i
	}

	// Build rows in explicit order first, then append remaining alphabetically.
	rows := make([]QuotaRow, 0, len(bucketByType))
	seen := make(map[string]bool, len(bucketByType))

	appendRow := func(rt string, b *quotav1alpha1.AllowanceBucket) {
		if seen[rt] {
			return
		}
		seen[rt] = true

		displayName := resourceTypeSuffix(rt)
		unit := "units"
		var divisor int64 = 1

		if m, ok := meta[rt]; ok {
			if m.DisplayName != "" {
				displayName = m.DisplayName
			}
			if m.Unit != "" {
				unit = m.Unit
			}
			if m.Divisor > 1 {
				divisor = m.Divisor
			}
		} else if rr, ok := rrByType[rt]; ok && rr.Spec.DisplayUnit != "" && rr.Spec.DisplayUnit != "1" {
			unit = rr.Spec.DisplayUnit
		}

		rows = append(rows, QuotaRow{
			ResourceType: rt,
			DisplayName:  displayName,
			Unit:         unit,
			Limit:        b.Status.Limit / divisor,
			Used:         b.Status.Allocated / divisor,
			Available:    b.Status.Available / divisor,
		})
	}

	for _, rt := range orderedTypes {
		if b, ok := bucketByType[rt]; ok {
			appendRow(rt, b)
		}
	}
	// Append any buckets not covered by orderedTypes.
	remaining := make([]string, 0)
	for rt := range bucketByType {
		if !seen[rt] {
			remaining = append(remaining, rt)
		}
	}
	// Stable alphabetical order for the tail.
	for i := 0; i < len(remaining)-1; i++ {
		for j := i + 1; j < len(remaining); j++ {
			if remaining[i] > remaining[j] {
				remaining[i], remaining[j] = remaining[j], remaining[i]
			}
		}
	}
	for _, rt := range remaining {
		appendRow(rt, bucketByType[rt])
	}

	return rows, nil
}

// resourceTypeSuffix derives a human-readable name from the last segment of a
// resource type string (e.g. "compute.datumapis.com/vcpus" → "vcpus").
func resourceTypeSuffix(resourceType string) string {
	if idx := strings.LastIndex(resourceType, "/"); idx >= 0 {
		return resourceType[idx+1:]
	}
	return resourceType
}
