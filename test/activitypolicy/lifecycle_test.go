// SPDX-License-Identifier: AGPL-3.0-only

package activitypolicy_test

import (
	"reflect"
	"testing"
)

const (
	instancePolicy           = policyDir + "instance-policy.yaml"
	workloadDeploymentPolicy = policyDir + "workloaddeployment-policy.yaml"

	cellNamespace      = "ns-1234"
	deploymentName     = "web-default-us-central-1"
	instanceName       = deploymentName + "-0"
	instanceInLocation = "Instance " + instanceName + " of workload web in us-central-1"
	projectWorkloadUID = "337901bf-e5b1-46fe-ad3f-5fc97631e0df"
)

// lifecycleEvent is an event as compute's cell controllers record it, after
// the activity exporter in the cell has stamped scope and source.
func lifecycleEvent(kind, name, reason, note string) map[string]any {
	event := map[string]any{
		"metadata": map[string]any{
			"name":      name + ".18a",
			"namespace": cellNamespace,
			"annotations": map[string]any{
				"platform.miloapis.com/scope.type":        "project",
				"platform.miloapis.com/scope.name":        "datum-cloud",
				"platform.miloapis.com/scope.namespace":   "default",
				"activity.miloapis.com/source-plane-type": "edge",
				"activity.miloapis.com/source-cluster":    "us-central-1-staging-lab",
				"activity.miloapis.com/source-region":     "us-central-1",
				"activity.miloapis.com/source-city":       "DFW",
			},
		},
		"reason": reason,
		"type":   "Normal",
		"regarding": map[string]any{
			"apiVersion": "compute.datumapis.com/v1alpha",
			"kind":       kind,
			"name":       name,
			"namespace":  cellNamespace,
			"uid":        "cell-uid",
		},
		"related": map[string]any{
			"apiVersion": "compute.datumapis.com/v1alpha",
			"kind":       "Workload",
			"name":       "web",
			"namespace":  "default",
			"uid":        projectWorkloadUID,
		},
		"reportingController": "compute.datumapis.com/instance-controller",
	}
	if note != "" {
		event["note"] = note
	}
	return event
}

func instanceEvent(reason, note string) map[string]any {
	return lifecycleEvent("Instance", instanceName, reason, note)
}

func scaledEvent() map[string]any {
	return lifecycleEvent("WorkloadDeployment", deploymentName, "Scaled", "Scaled from 2 to 3 replicas")
}

func TestInstanceLifecycle(t *testing.T) {
	p := loadPolicy(t, instancePolicy)
	for _, tc := range []struct {
		reason, note, want string
	}{
		{"Available", "Instance is ready", instanceInLocation + " started running"},
		{"Stopping", "Instance has stopped", instanceInLocation + " stopped"},
		{"ImageUnavailable", "pull access denied", instanceInLocation + " could not get its image: pull access denied"},
		{"ConfigurationError", "secret db not found",
			instanceInLocation + " could not start because of its configuration: secret db not found"},
		{"InstanceCrashing", "exited with code 1", instanceInLocation + " keeps crashing: exited with code 1"},
		{"Failed", "", instanceInLocation + " failed"},
		{"Terminating", "Scaling down to 1 replicas", instanceInLocation + " is shutting down: Scaling down to 1 replicas"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			_, summary := summarizeEvent(t, p, instanceEvent(tc.reason, tc.note))
			if summary != tc.want {
				t.Fatalf("summary = %q, want %q", summary, tc.want)
			}
		})
	}
}

func TestInstanceEventWithoutSource(t *testing.T) {
	event := instanceEvent("Available", "")
	annotations := event["metadata"].(map[string]any)["annotations"].(map[string]any)
	for _, k := range []string{"plane-type", "cluster", "region", "city"} {
		delete(annotations, "activity.miloapis.com/source-"+k)
	}

	_, summary := summarizeEvent(t, loadPolicy(t, instancePolicy), event)

	if want := "Instance " + instanceName + " of workload web started running"; summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
}

func TestOtherInstanceEventsAreNotRecorded(t *testing.T) {
	p := loadPolicy(t, instancePolicy)
	withoutRelated := instanceEvent("Available", "")
	delete(withoutRelated, "related")
	relatedDeployment := instanceEvent("Available", "")
	relatedDeployment["related"].(map[string]any)["kind"] = "WorkloadDeployment"

	for name, event := range map[string]map[string]any{
		"federation verification": instanceEvent("FederationVerification", ""),
		"quota warning":           instanceEvent("QuotaNoBudget", ""),
		"gate cleared":            instanceEvent("Ready", "All referenced companion ConfigMaps/Secrets are present"),
		"without related":         withoutRelated,
		"related deployment":      relatedDeployment,
	} {
		t.Run(name, func(t *testing.T) {
			if rule, summary := summarizeEvent(t, p, event); rule != "" {
				t.Fatalf("rule %s recorded %q", rule, summary)
			}
		})
	}
}

func TestWorkloadDeploymentScaled(t *testing.T) {
	_, summary := summarizeEvent(t, loadPolicy(t, workloadDeploymentPolicy), scaledEvent())

	if want := "Workload web in us-central-1: Scaled from 2 to 3 replicas"; summary != want {
		t.Fatalf("summary = %q, want %q", summary, want)
	}
}

func TestOtherWorkloadDeploymentEventsAreNotRecorded(t *testing.T) {
	p := loadPolicy(t, workloadDeploymentPolicy)
	withoutRelated := scaledEvent()
	delete(withoutRelated, "related")
	withoutNote := scaledEvent()
	delete(withoutNote, "note")

	for name, event := range map[string]map[string]any{
		"project paused":  lifecycleEvent("WorkloadDeployment", deploymentName, "ProjectPaused", ""),
		"without related": withoutRelated,
		"without note":    withoutNote,
	} {
		t.Run(name, func(t *testing.T) {
			if rule, summary := summarizeEvent(t, p, event); rule != "" {
				t.Fatalf("rule %s recorded %q", rule, summary)
			}
		})
	}
}

// Every lifecycle rule links the project's Workload, by the UID the user's own
// audit activity carries, so edge activity can be correlated with it.
func TestLifecycleEventsLinkTheProjectWorkload(t *testing.T) {
	type input struct {
		policy string
		event  map[string]any
	}
	cases := map[string]input{"Scaled": {workloadDeploymentPolicy, scaledEvent()}}
	for _, reason := range []string{
		"Available", "Stopping", "ImageUnavailable", "ConfigurationError", "InstanceCrashing", "Failed", "Terminating",
	} {
		cases[reason] = input{instancePolicy, instanceEvent(reason, "")}
	}

	want := map[string]any{
		"apiGroup":  "compute.datumapis.com",
		"kind":      "Workload",
		"name":      "web",
		"namespace": "default",
		"uid":       projectWorkloadUID,
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, links := summarizeEventWithLinks(t, loadPolicy(t, tc.policy), tc.event)
			if len(links) != 1 || !reflect.DeepEqual(links[0], want) {
				t.Fatalf("links = %v, want [%v]", links, want)
			}
		})
	}
}
