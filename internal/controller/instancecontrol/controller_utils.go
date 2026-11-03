package instancecontrol

import (
	"encoding/json"
	"fmt"
	"hash"
	"hash/fnv"

	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/dump"

	"go.datum.net/compute/api/v1alpha"
)

func ComputeHash(template v1alpha.InstanceTemplateSpec) string {
	hasher := fnv.New32a()
	_ = json.NewEncoder(hasher).Encode(template)

	return encodeHash(hasher)
}

func LegacyComputeHash(template v1alpha.InstanceTemplateSpec) string {
	hasher := fnv.New32a()
	_, _ = fmt.Fprintf(hasher, "%v", dump.ForHash(template))

	return encodeHash(hasher)
}

func encodeHash(hasher hash.Hash32) string {
	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum32()))
}

type TemplateHashes struct {
	Current string
	Legacy  string
}

func HashTemplate(template v1alpha.InstanceTemplateSpec) TemplateHashes {
	return TemplateHashes{
		Current: ComputeHash(template),
		Legacy:  LegacyComputeHash(template),
	}
}

func (h TemplateHashes) Matches(stored string) bool {
	return stored == h.Current || stored == h.Legacy
}

func (h TemplateHashes) IsLegacy(stored string) bool {
	return stored != h.Current && stored == h.Legacy
}
