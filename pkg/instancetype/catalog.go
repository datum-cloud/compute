// SPDX-License-Identifier: AGPL-3.0-only

// Package instancetype publishes the platform's instance type sizing.
//
// Sizing must be identical everywhere it is read: compute claims quota against
// these amounts before an instance is placed, and whatever runs the instance
// has to hand it the amounts that were claimed. A runtime that carried its own
// copy of the table would eventually bill one number and run another, so the
// catalog lives here and is imported rather than restated.
package instancetype

import "sort"

// D1Standard2 is the instance type name for the 1 vCPU / 2 GiB size that is
// the catalog baseline.
const D1Standard2 = "datumcloud/d1-standard-2"

// Resources are the dimensions a named instance type is sized at.
type Resources struct {
	// CPUMillicores is the number of CPU millicores (1000 = 1 vCPU).
	CPUMillicores int64

	// MemoryMiB is the amount of RAM in mebibytes.
	MemoryMiB int64
}

// catalog maps instance type names to their resource dimensions.
//
// These are the platform-declared sizes for the instance type, not a
// derivation of any infrastructure provider's machine type. (infra-provider-gcp
// separately maps datumcloud/d1-standard-2 to the GCP n2-standard-2 machine
// type for VM provisioning; that mapping does not define the size here.)
//
// Unexported and reached only through Lookup so a consumer — including one in
// another repository — cannot mutate platform sizing at runtime.
var catalog = map[string]Resources{
	D1Standard2: {
		CPUMillicores: 1000, // 1 vCPU
		MemoryMiB:     2048, // 2 GiB
	},
}

// Lookup returns the sizing for an instance type name, reporting whether the
// name is in the catalog. An unknown name yields no sizing rather than a
// fabricated default, so a typo surfaces as missing sizing instead of an
// instance quietly running at some other size than it was billed for.
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
