// SPDX-License-Identifier: AGPL-3.0-only

package webhook

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	admissionv1 "k8s.io/api/admission/v1"
	authenticationv1 "k8s.io/api/authentication/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	computev1alpha "go.datum.net/compute/api/v1alpha"
	"go.datum.net/compute/internal/features"
	"go.datum.net/compute/pkg/runtimeclass"
)

const sessionWebhookNamespace = "project-namespace"

func newSessionWebhook(objs ...client.Object) *instanceConsoleSessionWebhook {
	scheme := k8sruntime.NewScheme()
	if err := computev1alpha.AddToScheme(scheme); err != nil {
		panic(err)
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	return &instanceConsoleSessionWebhook{reader: fixedReader(c)}
}

func sessionWebhookContext(username string) context.Context {
	return admission.NewContextWithRequest(context.Background(), admission.Request{
		AdmissionRequest: admissionv1.AdmissionRequest{
			Operation: admissionv1.Create,
			UserInfo:  authenticationv1.UserInfo{Username: username},
		},
	})
}

func sessionWebhookInstance(class string) *computev1alpha.Instance {
	return &computev1alpha.Instance{
		ObjectMeta: metav1.ObjectMeta{Name: "web-0", Namespace: sessionWebhookNamespace, UID: "instance-uid"},
		Spec: computev1alpha.InstanceSpec{
			Runtime: computev1alpha.InstanceRuntimeSpec{
				Class: class,
				Sandbox: &computev1alpha.SandboxRuntime{
					Containers: []computev1alpha.SandboxContainer{{Name: "app"}},
				},
			},
		},
	}
}

func sessionWebhookClass(name string, features ...computev1alpha.RuntimeClassFeature) *computev1alpha.RuntimeClass {
	return &computev1alpha.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: computev1alpha.RuntimeClassSpec{
			Capabilities: computev1alpha.RuntimeClassCapabilities{Features: features},
		},
	}
}

func sessionWebhookSession() *computev1alpha.InstanceConsoleSession {
	return &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{Name: "web-0-abcde", Namespace: sessionWebhookNamespace},
		Spec: computev1alpha.InstanceConsoleSessionSpec{
			InstanceRef:   computev1alpha.InstanceConsoleSessionInstanceRef{Name: "web-0", UID: "instance-uid"},
			ContainerName: "app",
			Command:       []string{"sh"},
		},
	}
}

func newAdmittingSessionWebhook(t *testing.T) *instanceConsoleSessionWebhook {
	t.Helper()
	featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.InstanceConsoleSessions, true)
	return newSessionWebhook(
		sessionWebhookInstance("general-purpose"),
		sessionWebhookClass("general-purpose", runtimeclass.FeatureExec),
	)
}

func TestInstanceConsoleSessionWebhookDefaultRecordsRequester(t *testing.T) {
	w := newAdmittingSessionWebhook(t)
	session := sessionWebhookSession()
	session.Annotations = map[string]string{computev1alpha.InstanceConsoleSessionRequesterAnnotation: "someone-else"}

	require.NoError(t, w.Default(sessionWebhookContext("alice@example.com"), session))
	assert.Equal(t, "alice@example.com", session.Annotations[computev1alpha.InstanceConsoleSessionRequesterAnnotation])
}

func TestInstanceConsoleSessionWebhookDefaultAddsFinalizer(t *testing.T) {
	w := newAdmittingSessionWebhook(t)
	session := sessionWebhookSession()
	session.Finalizers = []string{"example.com/other"}

	require.NoError(t, w.Default(sessionWebhookContext("bob@example.com"), session))
	assert.Equal(t, []string{"example.com/other", computev1alpha.InstanceConsoleSessionFinalizer}, session.Finalizers,
		"a session deleted before the controller sees it is still held until its end is recorded")

	require.NoError(t, w.Default(sessionWebhookContext("bob@example.com"), session))
	assert.Len(t, session.Finalizers, 2, "defaulting twice adds the finalizer once")
}

func TestInstanceConsoleSessionWebhookValidateCreate(t *testing.T) {
	tests := []struct {
		name    string
		gateOff bool
		objs    []client.Object
		wantErr string
	}{
		{
			name: "admits a session for an exec-capable instance",
			objs: []client.Object{
				sessionWebhookInstance("general-purpose"),
				sessionWebhookClass("general-purpose", runtimeclass.FeatureExec),
			},
		},
		{
			name:    "refuses every session with the gate off",
			gateOff: true,
			objs: []client.Object{
				sessionWebhookInstance("general-purpose"),
				sessionWebhookClass("general-purpose", runtimeclass.FeatureExec),
			},
			wantErr: "shell sessions are not available in this environment",
		},
		{
			name:    "refuses a session for an instance that does not exist",
			objs:    []client.Object{sessionWebhookClass("general-purpose", runtimeclass.FeatureExec)},
			wantErr: "spec.instanceRef.name: Not found",
		},
		{
			name: "refuses a session for a class without exec",
			objs: []client.Object{
				sessionWebhookInstance("unikernel"),
				sessionWebhookClass("unikernel", runtimeclass.FeatureSandboxRuntime),
			},
			wantErr: `the "unikernel" runtime class does not support shell sessions`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			featuregatetesting.SetFeatureGateDuringTest(t, features.MutableFeatureGate, features.InstanceConsoleSessions, !tt.gateOff)

			w := newSessionWebhook(tt.objs...)
			ctx := sessionWebhookContext("alice@example.com")
			_, validateErr := w.ValidateCreate(ctx, sessionWebhookSession())
			defaultErr := w.Default(ctx, sessionWebhookSession())
			for phase, err := range map[string]error{"validating": validateErr, "mutating": defaultErr} {
				if tt.wantErr == "" {
					require.NoError(t, err, phase)
					continue
				}
				require.Error(t, err, "%s admission must refuse the session, because quota is claimed between the two phases", phase)
				assert.True(t, apierrors.IsInvalid(err), "%s: want a 422 Invalid, got %v", phase, err)
				assert.Contains(t, err.Error(), tt.wantErr, phase)
			}
		})
	}
}
