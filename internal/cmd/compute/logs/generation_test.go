// SPDX-License-Identifier: AGPL-3.0-only

package logs

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func genLine(ts int64, instance, label, id string) entry {
	return entry{ts: ts, stream: instance + id, labels: map[string]string{labelInstance: instance, label: id}}
}

func TestDiscoverGenerations(t *testing.T) {
	var base selector
	base.eq(labelWorkload, testWorkload)
	q := &fakeQuerier{pages: [][]entry{
		// a's current generation fills most of the page; b is a sandbox pod.
		{
			genLine(30, "a", labelVMGeneration, "a2"),
			genLine(29, "a", labelVMGeneration, "a2"),
			genLine(28, "b", labelPodGeneration, "b1"),
		},
		{genLine(10, "a", labelVMGeneration, "a1")},
		{line(5, "no generation label")},
	}}

	got, err := discoverGenerations(context.Background(), q, base, epoch, far, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := generations{
		"a": {{labelVMGeneration, "a2"}, {labelVMGeneration, "a1"}},
		"b": {{labelPodGeneration, "b1"}},
	}
	if diff := cmp.Diff(want, got, cmp.AllowUnexported(generation{})); diff != "" {
		t.Errorf("discoverGenerations() mismatch (-want +got):\n%s", diff)
	}

	wantQueries := []string{
		`{datum_workload_name="api"}`,
		`{datum_workload_name="api", container_id!="a2", k8s_pod_uid!="b1"}`,
		`{datum_workload_name="api", container_id!~"a2|a1", k8s_pod_uid!="b1", datum_instance_name!="a"}`,
	}
	gotQueries := make([]string, 0, len(q.queries))
	for _, r := range q.queries {
		gotQueries = append(gotQueries, r.query)
	}
	if diff := cmp.Diff(wantQueries, gotQueries); diff != "" {
		t.Errorf("queries mismatch (-want +got):\n%s", diff)
	}
}

func TestPickGeneration(t *testing.T) {
	vm := func(id string) generation { return generation{labelVMGeneration, id} }
	tests := []struct {
		name    string
		gens    generations
		index   int
		want    generationFilter
		wantErr bool
	}{
		{
			name:  "newest",
			gens:  generations{"b": {vm("b2")}, "a": {vm("a2"), vm("a1")}},
			index: 0,
			want:  generationFilter{label: labelVMGeneration, ids: []string{"a2", "b2"}},
		},
		{
			name:  "one before",
			gens:  generations{"b": {vm("b2")}, "a": {vm("a2"), vm("a1")}},
			index: 1,
			want:  generationFilter{label: labelVMGeneration, ids: []string{"a1"}, missing: []string{"b"}},
		},
		{
			name:    "mixed runtimes",
			gens:    generations{"a": {vm("a1")}, "b": {{labelPodGeneration, "b1"}}},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pickGeneration(tt.gens, tt.index)
			if (err != nil) != tt.wantErr {
				t.Fatalf("pickGeneration() error = %v, wantErr %v", err, tt.wantErr)
			}
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(generationFilter{})); diff != "" {
				t.Errorf("pickGeneration() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
