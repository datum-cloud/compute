// SPDX-License-Identifier: AGPL-3.0-only

package referenceddata

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

// fakeScheme is a minimal scheme with corev1 for LocalReader tests.
var fakeScheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = corev1.AddToScheme(s)
	return s
}()

func TestLocalReader_GetConfigMap_Found(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "app-config",
			Namespace: "default",
		},
		Data: map[string]string{"key": "value"},
	}
	cl := fake.NewClientBuilder().WithScheme(fakeScheme).WithObjects(cm).Build()
	r := NewLocalReader(cl)

	got, err := r.GetConfigMap(context.Background(), "ignored-project", "default", "app-config")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "app-config" {
		t.Errorf("got name %q, want %q", got.Name, "app-config")
	}
}

func TestLocalReader_GetConfigMap_NotFound(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(fakeScheme).Build()
	r := NewLocalReader(cl)

	_, err := r.GetConfigMap(context.Background(), "ignored-project", "default", "missing")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrSourceNotFound) {
		t.Errorf("expected ErrSourceNotFound, got: %v", err)
	}
}

func TestLocalReader_GetSecret_Found(t *testing.T) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "db-creds",
			Namespace: "prod",
		},
		Data: map[string][]byte{"password": []byte("secret!")},
	}
	cl := fake.NewClientBuilder().WithScheme(fakeScheme).WithObjects(secret).Build()
	r := NewLocalReader(cl)

	got, err := r.GetSecret(context.Background(), "ignored", "prod", "db-creds")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Name != "db-creds" {
		t.Errorf("got name %q, want %q", got.Name, "db-creds")
	}
}

func TestLocalReader_GetSecret_NotFound(t *testing.T) {
	cl := fake.NewClientBuilder().WithScheme(fakeScheme).Build()
	r := NewLocalReader(cl)

	_, err := r.GetSecret(context.Background(), "ignored", "default", "missing-secret")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrSourceNotFound) {
		t.Errorf("expected ErrSourceNotFound, got: %v", err)
	}
}

func TestClassifyError_NotFound(t *testing.T) {
	notFound := apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, "foo")
	err := classifyError(notFound, "ConfigMap", "ns", "foo")
	if !errors.Is(err, ErrSourceNotFound) {
		t.Errorf("expected ErrSourceNotFound, got: %v", err)
	}
}

func TestClassifyError_Forbidden(t *testing.T) {
	forbidden := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "bar", errors.New("denied"))
	err := classifyError(forbidden, "Secret", "ns", "bar")
	if !errors.Is(err, ErrSourceUnauthorized) {
		t.Errorf("expected ErrSourceUnauthorized, got: %v", err)
	}
}

func TestClassifyError_Unauthorized(t *testing.T) {
	unauthorized := apierrors.NewUnauthorized("no token")
	err := classifyError(unauthorized, "Secret", "ns", "bar")
	if !errors.Is(err, ErrSourceUnauthorized) {
		t.Errorf("expected ErrSourceUnauthorized, got: %v", err)
	}
}

func TestClassifyError_Other(t *testing.T) {
	other := errors.New("something else broke")
	err := classifyError(other, "ConfigMap", "ns", "foo")
	if errors.Is(err, ErrSourceNotFound) || errors.Is(err, ErrSourceUnauthorized) {
		t.Errorf("unexpected sentinel error classification for %v", err)
	}
	if !errors.Is(err, other) {
		t.Errorf("expected original error to be preserved, got %v", err)
	}
}

func TestLocalReader_GetConfigMap_Forbidden(t *testing.T) {
	gr := schema.GroupResource{Resource: "configmaps"}
	cl := fake.NewClientBuilder().
		WithScheme(fakeScheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				return apierrors.NewForbidden(gr, key.Name, errors.New("access denied"))
			},
		}).
		Build()
	r := NewLocalReader(cl)

	_, err := r.GetConfigMap(context.Background(), "ignored", "default", "secret-cfg")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, ErrSourceUnauthorized) {
		t.Errorf("expected ErrSourceUnauthorized, got: %v", err)
	}
}
