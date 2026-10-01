// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"
)

const (
	// generationLookback is the default window for --current and --previous.
	generationLookback = 7 * 24 * time.Hour

	generationRounds    = 30
	generationPageLimit = 1000
)

// generation is one VM or pod incarnation of an instance. It changes when the
// instance is replaced or redeployed, not when it restarts in place.
type generation struct {
	label string
	id    string
}

func generationOf(labels map[string]string) generation {
	if id := labels[labelVMGeneration]; id != "" {
		return generation{labelVMGeneration, id}
	}
	if id := labels[labelPodGeneration]; id != "" {
		return generation{labelPodGeneration, id}
	}
	return generation{}
}

// generations maps instance name to its generations, newest first.
type generations map[string][]generation

// discoverGenerations finds up to need generations per instance matching base.
//
// Scanning backward would stall on a chatty generation filling every page, so
// each round excludes the generations found and the instances satisfied so
// far; the newest remaining line then always belongs to a new generation.
func discoverGenerations(ctx context.Context, q querier, base selector, start, end time.Time, need int) (generations, error) {
	gens := generations{}
	known := map[string][]string{}
	var satisfied []string

	for range generationRounds {
		sel := base.clone()
		sel.noneOf(labelVMGeneration, known[labelVMGeneration])
		sel.noneOf(labelPodGeneration, known[labelPodGeneration])
		sel.noneOf(labelInstance, satisfied)

		page, err := q.queryRange(ctx, queryRequest{query: sel.String(), start: start, end: end, limit: generationPageLimit, dir: backward})
		if err != nil {
			return nil, fmt.Errorf("finding instance generations: %w", err)
		}

		found := false
		for _, e := range page {
			inst := e.labels[labelInstance]
			g := generationOf(e.labels)
			if inst == "" || g.id == "" || len(gens[inst]) >= need || slices.Contains(gens[inst], g) {
				continue
			}
			gens[inst] = append(gens[inst], g)
			known[g.label] = append(known[g.label], g.id)
			if len(gens[inst]) == need {
				satisfied = append(satisfied, inst)
			}
			found = true
		}
		// Either nothing is left, or only lines without a generation.
		if !found {
			break
		}
	}
	return gens, nil
}

// generationFilter restricts a query to one generation per instance.
type generationFilter struct {
	label   string
	ids     []string
	missing []string // instances with no generation at the requested index
}

func (f generationFilter) apply(sel *selector) {
	sel.oneOf(f.label, f.ids)
}

// pickGeneration selects each instance's newest (index 0) or previous
// (index 1) generation.
func pickGeneration(gens generations, index int) (generationFilter, error) {
	instances := make([]string, 0, len(gens))
	for inst := range gens {
		instances = append(instances, inst)
	}
	sort.Strings(instances)

	var f generationFilter
	for _, inst := range instances {
		if index >= len(gens[inst]) {
			f.missing = append(f.missing, inst)
			continue
		}
		g := gens[inst][index]
		// One selector can't OR two labels, and a workload runs on one
		// runtime, so this never happens in practice.
		if f.label != "" && f.label != g.label {
			return generationFilter{}, errors.New("instances report both VM and pod generations; narrow with --instance")
		}
		f.label = g.label
		f.ids = append(f.ids, g.id)
	}
	return f, nil
}
