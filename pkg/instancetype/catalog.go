// SPDX-License-Identifier: AGPL-3.0-only

// Package instancetype publishes the platform's instance type sizing.
//
// Sizing must be identical everywhere it is read. Compute claims quota against
// these amounts before an instance is placed, and the runtime must then give
// the instance the amounts that were claimed. A runtime holding its own copy of
// the table could bill one number and run another, so every consumer imports
// this catalog instead of restating it.
package instancetype

import "sort"

// D1Standard2 is the catalog's baseline instance type name.
const D1Standard2 = "datumcloud/d1-standard-2"

// Resources are the dimensions a named instance type is sized at.
type Resources struct {
	// CPUMillicores is the CPU allocation in millicores. 1000 millicores is
	// one virtual CPU (vCPU).
	CPUMillicores int64

	// MemoryMiB is the memory allocation in mebibytes (MiB).
	MemoryMiB int64
}

// catalog holds the platform-declared sizes, which are not derived from any
// infrastructure provider's machine type. For example, infra-provider-gcp maps
// datumcloud/d1-standard-2 to the GCP n2-standard-2 machine type, but that
// mapping does not define the size here.
//
// The map stays unexported and is reached only through Lookup, so no consumer
// can mutate platform sizing at runtime.
var catalog = map[string]Resources{
	D1Standard2: {
		CPUMillicores: 1000, // 1 vCPU
		MemoryMiB:     2048, // 2 GiB
	},
}

// Lookup returns the sizing for an instance type name and reports whether the
// catalog contains the name. An unknown name yields no sizing rather than a
// default, so a misspelled name surfaces as missing sizing instead of an
// instance running at a size it was not billed for.
func Lookup(name string) (Resources, bool) {
	res, ok := catalog[name]
	return res, ok
}

// Names returns the catalogued instance type names in sorted order.
func Names() []string {
	names := make([]string, 0, len(catalog))
	for name := range catalog {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
