// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	featuregatetesting "k8s.io/component-base/featuregate/testing"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	"go.datum.net/compute/pkg/runtimeclass"
)

const (
	sessionAdmissionInstanceName = "web-0"
	sessionAdmissionContainer    = "app"
	sessionAdmissionRefField     = "spec.instanceRef"
)

func sessionAdmissionInstance(mutate ...func(*computev1alpha.Instance)) *computev1alpha.Instance {
	instance := &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: sessionAdmissionInstanceName, Namespace: testDefaultNamespace, UID: "instance-uid"},
		Spec: computev1alpha.InstanceSpec{
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Class: "general-purpose",
				Sandbox: &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{{Name: sessionAdmissionContainer}, {Name: "sidecar"}},
				},
			},
		},
	}
	for _, m := range mutate {
		m(instance)
	}
	return instance
}

func sessionAdmissionRequest() *computev1alpha.InstanceConsoleSession {
	return &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{Name: "web-0-abcde", Namespace: testDefaultNamespace},
		Spec: computev1alpha.InstanceConsoleSessionSpec{
			InstanceRef:   computev1alpha.InstanceConsoleSessionInstanceRef{Name: sessionAdmissionInstanceName, UID: "instance-uid"},
			ContainerName: sessionAdmissionContainer,
			Command:       []string{"sh"},
		},
	}
}

func sessionAdmissionCatalog() runtimeclass.Catalog {
	return runtimeclass.Catalog{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "general-purpose"},
			Spec: computev1alpha.RuntimeClassSpec{
				Default: true,
				Capabilities: computev1alpha.RuntimeClassCapabilities{
					Features: []computev1alpha.RuntimeClassFeature{runtimeclass.FeatureSandboxRuntime, runtimeclass.FeatureExec},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "unikernel"},
			Spec: computev1alpha.RuntimeClassSpec{
				Capabilities: computev1alpha.RuntimeClassCapabilities{
					Features: []computev1alpha.RuntimeClassFeature{runtimeclass.FeatureSandboxRuntime},
				},
			},
		},
	}
}

func TestValidateInstanceConsoleSessionCreate(t *testing.T) {
	deleting := metav1.Now()

	tests := []struct {
		name      string
		gateOff   bool
		session   func(*computev1alpha.InstanceConsoleSession)
		instance  *computev1alpha.Instance
		noCatalog bool
		wantType  field.ErrorType
		wantField string
		wantMsg   string
	}{
		{
			name:     "a session for a container in an exec-capable instance is admitted",
			instance: sessionAdmissionInstance(),
		},
		{
			name: "an instance without a class uses the catalog default",
			instance: sessionAdmissionInstance(func(i *computev1alpha.Instance) {
				i.Spec.Runtime.Class = ""
			}),
		},
		{
			name:      "the feature gate off refuses every session",
			gateOff:   true,
			instance:  sessionAdmissionInstance(),
			wantType:  field.ErrorTypeForbidden,
			wantField: "spec",
			wantMsg:   "not available in this environment",
		},
		{
			name:      "a missing instance is refused",
			wantType:  field.ErrorTypeNotFound,
			wantField: "spec.instanceRef.name",
		},
		{
			name: "a replaced instance is refused",
			session: func(s *computev1alpha.InstanceConsoleSession) {
				s.Spec.InstanceRef.UID = "old-uid"
			},
			instance:  sessionAdmissionInstance(),
			wantType:  field.ErrorTypeInvalid,
			wantField: "spec.instanceRef.uid",
			wantMsg:   "has been replaced",
		},
		{
			name: "an instance being deleted is refused",
			instance: sessionAdmissionInstance(func(i *computev1alpha.Instance) {
				i.DeletionTimestamp = &deleting
			}),
			wantType:  field.ErrorTypeInvalid,
			wantField: "spec.instanceRef.name",
			wantMsg:   "being deleted",
		},
		{
			name: "a container the instance does not run is refused with the choices",
			session: func(s *computev1alpha.InstanceConsoleSession) {
				s.Spec.ContainerName = "db"
			},
			instance:  sessionAdmissionInstance(),
			wantType:  field.ErrorTypeNotSupported,
			wantField: "spec.containerName",
			wantMsg:   `"app", "sidecar"`,
		},
		{
			name: "a virtual machine instance is refused",
			instance: sessionAdmissionInstance(func(i *computev1alpha.Instance) {
				i.Spec.Runtime.Sandbox = nil
				i.Spec.Runtime.VirtualMachine = &computev1alpha.VirtualMachineRuntime{}
			}),
			wantType:  field.ErrorTypeInvalid,
			wantField: "spec.containerName",
			wantMsg:   "virtual machine",
		},
		{
			name: "a class without exec is refused",
			instance: sessionAdmissionInstance(func(i *computev1alpha.Instance) {
				i.Spec.Runtime.Class = "unikernel"
			}),
			wantType:  field.ErrorTypeForbidden,
			wantField: sessionAdmissionRefField,
			wantMsg:   `the "unikernel" runtime class does not support shell sessions`,
		},
		{
			name: "a class missing from the catalog is refused",
			instance: sessionAdmissionInstance(func(i *computev1alpha.Instance) {
				i.Spec.Runtime.Class = "retired"
			}),
			wantType:  field.ErrorTypeForbidden,
			wantField: sessionAdmissionRefField,
			wantMsg:   `the "retired" runtime class does not support shell sessions`,
		},
		{
			name: "the requester annotation a client sends is admitted, since admission overwrites it",
			session: func(s *computev1alpha.InstanceConsoleSession) {
				s.Annotations = map[string]string{computev1alpha.InstanceConsoleSessionRequesterAnnotation: "someone-else"}
			},
			instance: sessionAdmissionInstance(),
		},
		{
			name: "metadata outside compute's namespaces is admitted",
			session: func(s *computev1alpha.InstanceConsoleSession) {
				s.Annotations = map[string]string{"example.com/ticket": "OPS-1", "notcompute.datumapis.com/x": "y"}
				s.Labels = map[string]string{"team": "web", "app.kubernetes.io/name": "shell"}
			},
			instance: sessionAdmissionInstance(),
		},
		{
			name: "a client-set event marker annotation is refused",
			session: func(s *computev1alpha.InstanceConsoleSession) {
				s.Annotations = map[string]string{"compute.datumapis.com/sessionended-event": "recorded"}
			},
			instance:  sessionAdmissionInstance(),
			wantType:  field.ErrorTypeForbidden,
			wantField: "metadata.annotations[compute.datumapis.com/sessionended-event]",
			wantMsg:   "set by the platform",
		},
		{
			name: "a client-set annotation in a compute subdomain is refused",
			session: func(s *computev1alpha.InstanceConsoleSession) {
				s.Annotations = map[string]string{"shell-agent.compute.datumapis.com/endpoint": "abc"}
			},
			instance:  sessionAdmissionInstance(),
			wantType:  field.ErrorTypeForbidden,
			wantField: "metadata.annotations[shell-agent.compute.datumapis.com/endpoint]",
		},
		{
			name: "a client-set compute label is refused",
			session: func(s *computev1alpha.InstanceConsoleSession) {
				s.Labels = map[string]string{computev1alpha.InstanceConsoleSessionUIDLabel: "forged"}
			},
			instance:  sessionAdmissionInstance(),
			wantType:  field.ErrorTypeForbidden,
			wantField: "metadata.labels[" + computev1alpha.InstanceConsoleSessionUIDLabel + "]",
		},
		{
			name:      "an empty catalog refuses the session",
			instance:  sessionAdmissionInstance(),
			noCatalog: true,
			wantType:  field.ErrorTypeForbidden,
			wantField: sessionAdmissionRefField,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.InstanceConsoleSessions, !tt.gateOff)

			session := sessionAdmissionRequest()
			if tt.session != nil {
				tt.session(session)
			}
			opts := InstanceConsoleSessionValidationOptions{Instance: tt.instance}
			if !tt.noCatalog {
				opts.RuntimeClasses = sessionAdmissionCatalog()
			}

			errs := ValidateInstanceConsoleSessionCreate(session, opts)
			if tt.wantType == "" {
				assert.Empty(t, errs)
				return
			}
			require.Len(t, errs, 1, errs.ToAggregate())
			assert.Equal(t, tt.wantType, errs[0].Type)
			assert.Equal(t, tt.wantField, errs[0].Field)
			assert.Contains(t, errs[0].Error(), tt.wantMsg)
		})
	}
}
