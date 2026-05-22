// Package revision manages workload revision history stored in ConfigMaps.
// Each workload maintains a ConfigMap keyed by "compute.datumapis.com-revision-history.<workloadName>"
// whose data map holds JSON-encoded Entry values keyed by revision number string.
package revision

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	// CurrentRevisionAnnotation is the annotation key on the revision ConfigMap
	// that stores the active revision number as a string.
	CurrentRevisionAnnotation = "compute.datumapis.com/current-revision"

	// ConfigMapNamePrefix is the prefix for revision history ConfigMap names.
	ConfigMapNamePrefix = "compute.datumapis.com-revision-history."

	// MaxRevisions is the maximum number of revision entries to retain.
	MaxRevisions = 20
)

// Entry is one revision record stored as JSON in the ConfigMap's data map.
type Entry struct {
	Rev          int    `json:"rev"`
	Timestamp    string `json:"timestamp"`
	Image        string `json:"image"`
	Changes      string `json:"changes"`
	Actor        string `json:"actor"`
	TemplateHash string `json:"templateHash"`
	// SpecJSON holds a JSON-encoded WorkloadSpec for use by rollback.
	SpecJSON string `json:"spec"`
}

// ConfigMapName returns the ConfigMap name for the given workload.
func ConfigMapName(workloadName string) string {
	return ConfigMapNamePrefix + workloadName
}

// CurrentRevision returns the current revision number from the ConfigMap annotation.
// Returns 0 if no history ConfigMap exists or the annotation is absent.
func CurrentRevision(ctx context.Context, c client.Client, namespace, workloadName string) int {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ConfigMapName(workloadName)}, &cm); err != nil {
		return 0
	}
	if v, ok := cm.Annotations[CurrentRevisionAnnotation]; ok {
		n, err := strconv.Atoi(v)
		if err == nil {
			return n
		}
	}
	return 0
}

// WriteEntry creates or updates the revision history ConfigMap with entry.
// It enforces MaxRevisions by dropping the entry with the lowest revision number
// when the cap is exceeded. It updates the CurrentRevisionAnnotation.
func WriteEntry(ctx context.Context, c client.Client, namespace, workloadName string, entry Entry) error {
	cmName := ConfigMapName(workloadName)

	var cm corev1.ConfigMap
	exists := true
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: cmName}, &cm); err != nil {
		if !k8serrors.IsNotFound(err) {
			return fmt.Errorf("getting revision ConfigMap: %w", err)
		}
		exists = false
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Namespace:   namespace,
				Name:        cmName,
				Annotations: map[string]string{},
			},
			Data: map[string]string{},
		}
	}

	if cm.Data == nil {
		cm.Data = map[string]string{}
	}
	if cm.Annotations == nil {
		cm.Annotations = map[string]string{}
	}

	// Marshal the new entry.
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshalling revision entry: %w", err)
	}
	cm.Data[strconv.Itoa(entry.Rev)] = string(data)

	// Enforce cap: remove the lowest-numbered key until within MaxRevisions.
	for len(cm.Data) > MaxRevisions {
		lowest := -1
		for k := range cm.Data {
			n, err := strconv.Atoi(k)
			if err != nil {
				continue
			}
			if lowest < 0 || n < lowest {
				lowest = n
			}
		}
		if lowest >= 0 {
			delete(cm.Data, strconv.Itoa(lowest))
		} else {
			break
		}
	}

	cm.Annotations[CurrentRevisionAnnotation] = strconv.Itoa(entry.Rev)

	if exists {
		if err := c.Update(ctx, &cm); err != nil {
			return fmt.Errorf("updating revision ConfigMap: %w", err)
		}
	} else {
		if err := c.Create(ctx, &cm); err != nil {
			return fmt.Errorf("creating revision ConfigMap: %w", err)
		}
	}

	return nil
}

// ReadEntries returns all revision entries sorted by Rev descending, and the
// current revision number. Returns an empty slice (not an error) when no
// history ConfigMap exists.
func ReadEntries(ctx context.Context, c client.Client, namespace, workloadName string) (entries []Entry, currentRev int, err error) {
	var cm corev1.ConfigMap
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ConfigMapName(workloadName)}, &cm); err != nil {
		if k8serrors.IsNotFound(err) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("getting revision ConfigMap: %w", err)
	}

	if v, ok := cm.Annotations[CurrentRevisionAnnotation]; ok {
		n, err := strconv.Atoi(v)
		if err == nil {
			currentRev = n
		}
	}

	for _, v := range cm.Data {
		var e Entry
		if err := json.Unmarshal([]byte(v), &e); err != nil {
			// Skip malformed entries.
			continue
		}
		entries = append(entries, e)
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Rev > entries[j].Rev
	})

	return entries, currentRev, nil
}
