package instancecontrol

import (
	"fmt"
	"hash"
	"hash/fnv"

	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/utils/dump"
)

// ComputeHash returns a hash value calculated from pod template and
// a collisionCount to avoid hash collision. The hash will be safe encoded to
// avoid bad words.
//
// Inspired by: https://github.com/kubernetes/kubernetes/blob/8aae5398b3885dc271d407c4d661e19653daaf88/pkg/controller/controller_utils.go#L1295
func ComputeHash(obj interface{}) string {
	hasher := fnv.New32a()
	DeepHashObject(hasher, obj)

	return rand.SafeEncodeString(fmt.Sprint(hasher.Sum32()))
}

// DeepHashObject writes specified object to hash using the spew library
// which follows pointers and prints actual values of the nested objects
// ensuring the hash does not change when a pointer changes.
func DeepHashObject(hasher hash.Hash, objectToWrite interface{}) {
	hasher.Reset()
	_, _ = fmt.Fprintf(hasher, "%v", dump.ForHash(objectToWrite))
}
