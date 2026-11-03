// SPDX-License-Identifier: AGPL-3.0-only

package shellagent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// KeySecretKey is the key in an endpoint's Secret that holds its raw
	// 32-byte identity key.
	KeySecretKey = "key"

	annotationKeyCreated = annotationPrefix + "key-created-at"
	rotationLockName     = "endpoint-key-rotation"
	rotationLockDuration = 2 * time.Hour
	endpointRestartWait  = 5 * time.Minute
)

var rotationPollInterval = 5 * time.Second

type identity struct {
	endpointID string
	created    time.Time
}

// KeySecretName is the Secret holding the identity key of the tunnel endpoint
// with the given ordinal.
func KeySecretName(ordinal int) string {
	return fmt.Sprintf("endpoint-key-%d", ordinal)
}

// EndpointID returns the iroh endpoint ID of a raw ed25519 identity key.
func EndpointID(seed []byte) string {
	return hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
}

func newSeed() ([]byte, error) {
	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return nil, err
	}
	return seed, nil
}

// EnsureIdentity creates the paired endpoint's key Secret if it is missing and
// loads the endpoint ID the agent publishes.
func (a *Agent) EnsureIdentity(ctx context.Context) error {
	key := client.ObjectKey{Namespace: a.cfg.Namespace, Name: KeySecretName(a.cfg.Ordinal)}
	var secret corev1.Secret
	err := a.cell.Get(ctx, key, &secret)
	if apierrors.IsNotFound(err) {
		var seed []byte
		if seed, err = newSeed(); err != nil {
			return err
		}
		secret = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:        key.Name,
				Namespace:   key.Namespace,
				Labels:      map[string]string{componentLabel: "shell-endpoint-key"},
				Annotations: map[string]string{annotationKeyCreated: a.now().UTC().Format(time.RFC3339)},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{KeySecretKey: seed},
		}
		err = a.cell.Create(ctx, &secret)
		if apierrors.IsAlreadyExists(err) {
			err = a.cell.Get(ctx, key, &secret)
		}
	}
	if err != nil {
		return fmt.Errorf("load endpoint key %s: %w", key.Name, err)
	}
	return a.loadIdentity(&secret)
}

func (a *Agent) loadIdentity(secret *corev1.Secret) error {
	seed := secret.Data[KeySecretKey]
	if len(seed) != ed25519.SeedSize {
		return fmt.Errorf("endpoint key %s holds %d bytes, want %d", secret.Name, len(seed), ed25519.SeedSize)
	}
	created := secret.CreationTimestamp.Time
	if at, err := time.Parse(time.RFC3339, secret.Annotations[annotationKeyCreated]); err == nil {
		created = at
	}
	a.identity.Store(&identity{endpointID: EndpointID(seed), created: created})
	return nil
}

// RunKeyRotation replaces the endpoint's identity key once it is older than
// the rotation period, checking hourly until ctx ends.
func (a *Agent) RunKeyRotation(ctx context.Context) error {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		if id := a.identity.Load(); id != nil && a.now().Sub(id.created) >= a.cfg.KeyRotationPeriod {
			if err := a.rotate(ctx); err != nil && ctx.Err() == nil {
				log.FromContext(ctx).Error(err, "rotate endpoint key")
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// rotate stops claiming, waits for open sessions to end or expire, replaces
// the key, restarts the endpoint so it serves the new key, and resumes. One
// agent in the cell rotates at a time, so the cell keeps taking sessions.
func (a *Agent) rotate(ctx context.Context) error {
	locked, err := a.lockRotation(ctx)
	if err != nil || !locked {
		return err
	}
	defer a.unlockRotation(ctx)
	a.rotating.Store(true)
	defer a.rotating.Store(false)
	logger := log.FromContext(ctx)
	logger.Info("rotating endpoint key; waiting for open sessions to end")

	idle := 0
	for idle < 2 {
		if a.draining.Load() {
			return nil
		}
		if a.openCount() == 0 && len(a.liveSessions()) == 0 {
			idle++
		} else {
			idle = 0
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(rotationPollInterval):
		}
	}

	previous := a.EndpointID()
	key := client.ObjectKey{Namespace: a.cfg.Namespace, Name: KeySecretName(a.cfg.Ordinal)}
	var secret corev1.Secret
	if err := a.cell.Get(ctx, key, &secret); err != nil {
		return err
	}
	seed, err := newSeed()
	if err != nil {
		return err
	}
	secret.Data = map[string][]byte{KeySecretKey: seed}
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[annotationKeyCreated] = a.now().UTC().Format(time.RFC3339)
	if err := a.cell.Update(ctx, &secret); err != nil {
		return err
	}
	if err := a.loadIdentity(&secret); err != nil {
		return err
	}
	a.releaseLease(ctx, previous)
	logger.Info("replaced endpoint key", "endpointID", a.EndpointID())
	return a.restartEndpoint(ctx)
}

// restartEndpoint deletes the paired endpoint's pod and waits for its
// replacement to be ready with the new key.
func (a *Agent) restartEndpoint(ctx context.Context) error {
	if a.cfg.EndpointPodName == "" {
		return nil
	}
	key := client.ObjectKey{Namespace: a.cfg.Namespace, Name: a.cfg.EndpointPodName}
	var pod corev1.Pod
	if err := a.cell.Get(ctx, key, &pod); client.IgnoreNotFound(err) != nil {
		return err
	}
	previous := pod.UID
	if previous != "" {
		if err := a.cell.Delete(ctx, &pod, client.Preconditions{UID: &previous}); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, endpointRestartWait)
	defer cancel()
	for {
		var next corev1.Pod
		err := a.cell.Get(waitCtx, key, &next)
		if err == nil && next.UID != previous && podReady(&next) {
			return nil
		}
		select {
		case <-waitCtx.Done():
			return fmt.Errorf("endpoint %s did not become ready after its key rotated", key.Name)
		case <-time.After(2 * time.Second):
		}
	}
}

// endpointReady reports whether the paired endpoint can carry a connection. A
// session claimed while it cannot fails at the client's dial.
func (a *Agent) endpointReady(ctx context.Context) (bool, error) {
	if a.cfg.EndpointPodName == "" {
		return true, nil
	}
	var pod corev1.Pod
	err := a.cell.Get(ctx, client.ObjectKey{Namespace: a.cfg.Namespace, Name: a.cfg.EndpointPodName}, &pod)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return podReady(&pod), nil
}

func podReady(pod *corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (a *Agent) lockRotation(ctx context.Context) (bool, error) {
	now := metav1.NewMicroTime(a.now())
	lock := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{Name: rotationLockName, Namespace: a.cfg.Namespace},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       ptr.To(a.incarnation),
			LeaseDurationSeconds: ptr.To(int32(rotationLockDuration.Seconds())),
			AcquireTime:          &now,
			RenewTime:            &now,
		},
	}
	err := a.cell.Create(ctx, lock)
	if err == nil {
		return true, nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return false, err
	}
	var held coordinationv1.Lease
	if err := a.cell.Get(ctx, client.ObjectKeyFromObject(lock), &held); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	if held.Spec.RenewTime != nil && a.now().Before(held.Spec.RenewTime.Add(rotationLockDuration)) {
		return false, nil
	}
	_ = a.cell.Delete(ctx, &held, client.Preconditions{UID: &held.UID})
	return false, nil
}

func (a *Agent) unlockRotation(ctx context.Context) {
	err := a.cell.Delete(ctx, &coordinationv1.Lease{ObjectMeta: metav1.ObjectMeta{
		Name:      rotationLockName,
		Namespace: a.cfg.Namespace,
	}})
	if client.IgnoreNotFound(err) != nil {
		log.FromContext(ctx).Error(err, "release key rotation lock")
	}
}
