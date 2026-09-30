// SPDX-License-Identifier: AGPL-3.0-only

package validation

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// TestInstanceConsoleSessionCRD covers the rules the API server enforces on a
// shell session before anything serves it: the spec cannot change, and the
// command, key and lifetime stay within their published limits.
func TestInstanceConsoleSessionCRD(t *testing.T) {
	ctx := context.Background()
	c := startRuntimeClassEnvtest(t)

	create := func(t *testing.T, tweak func(*computev1alpha.InstanceConsoleSession)) (*computev1alpha.InstanceConsoleSession, error) {
		t.Helper()
		session := newConsoleSession()
		if tweak != nil {
			tweak(session)
		}
		err := c.Create(ctx, session)
		if err == nil {
			t.Cleanup(func() { _ = c.Delete(ctx, session) })
		}
		return session, err
	}

	t.Run("a session starts pending with the default lifetime", func(t *testing.T) {
		session, err := create(t, nil)
		require.NoError(t, err)

		require.NotNil(t, session.Spec.TTL)
		require.Equal(t, 15*time.Minute, session.Spec.TTL.Duration)

		require.Len(t, session.Status.Conditions, 1)
		ready := session.Status.Conditions[0]
		require.Equal(t, computev1alpha.InstanceConsoleSessionReady, ready.Type)
		require.Equal(t, metav1.ConditionUnknown, ready.Status)
		require.Equal(t, computev1alpha.InstanceConsoleSessionReasonPending, ready.Reason)
	})

	t.Run("the spec cannot be changed", func(t *testing.T) {
		session, err := create(t, nil)
		require.NoError(t, err)

		session.Spec.Command = []string{"/bin/bash"}
		require.ErrorContains(t, c.Update(ctx, session), "spec is immutable")
	})

	t.Run("status can be written", func(t *testing.T) {
		session, err := create(t, nil)
		require.NoError(t, err)

		session.Status.Connection = &computev1alpha.InstanceConsoleSessionConnection{
			EndpointID: strings.Repeat("ab", 32),
			RelayURLs:  []string{"https://relay.example.com"},
			Target:     "exec-agent-0.exec-agent.compute-shell-system.svc.cluster.local:7777",
		}
		require.NoError(t, c.Status().Update(ctx, session))
	})

	t.Run("a command of exactly 16 KiB is accepted", func(t *testing.T) {
		_, err := create(t, func(s *computev1alpha.InstanceConsoleSession) {
			s.Spec.Command = []string{strings.Repeat("a", 8192), strings.Repeat("b", 8192)}
		})
		require.NoError(t, err)
	})

	t.Run("a command over 16 KiB is refused", func(t *testing.T) {
		_, err := create(t, func(s *computev1alpha.InstanceConsoleSession) {
			s.Spec.Command = []string{strings.Repeat("a", 8192), strings.Repeat("b", 8193)}
		})
		require.ErrorContains(t, err, "command must total at most 16 KiB")
	})

	t.Run("the command limit counts bytes, not characters", func(t *testing.T) {
		_, err := create(t, func(s *computev1alpha.InstanceConsoleSession) {
			s.Spec.Command = []string{strings.Repeat("é", 8193)}
		})
		require.ErrorContains(t, err, "command must total at most 16 KiB")
	})

	t.Run("a session needs a command", func(t *testing.T) {
		_, err := create(t, func(s *computev1alpha.InstanceConsoleSession) {
			s.Spec.Command = nil
		})
		require.Error(t, err)
	})

	t.Run("a command has at most 64 arguments", func(t *testing.T) {
		_, err := create(t, func(s *computev1alpha.InstanceConsoleSession) {
			s.Spec.Command = make([]string, 65)
			for i := range s.Spec.Command {
				s.Spec.Command[i] = "x"
			}
		})
		require.Error(t, err)
	})

	for name, ttl := range map[string]time.Duration{
		"one minute": time.Minute,
		"one hour":   time.Hour,
	} {
		t.Run("a lifetime of "+name+" is accepted", func(t *testing.T) {
			_, err := create(t, func(s *computev1alpha.InstanceConsoleSession) {
				s.Spec.TTL = &metav1.Duration{Duration: ttl}
			})
			require.NoError(t, err)
		})
	}

	for name, ttl := range map[string]time.Duration{
		"under a minute": 59 * time.Second,
		"over an hour":   time.Hour + time.Second,
	} {
		t.Run("a lifetime "+name+" is refused", func(t *testing.T) {
			_, err := create(t, func(s *computev1alpha.InstanceConsoleSession) {
				s.Spec.TTL = &metav1.Duration{Duration: ttl}
			})
			require.ErrorContains(t, err, "ttl must be between 1m and 1h")
		})
	}

	for name, key := range map[string]string{
		"uppercase":  strings.Repeat("AB", 32),
		"too short":  strings.Repeat("ab", 31),
		"not hex":    strings.Repeat("zz", 32),
		"prefixed":   "0x" + strings.Repeat("ab", 31),
		"base64-ish": strings.Repeat("a+", 32),
	} {
		t.Run("a client key that is "+name+" is refused", func(t *testing.T) {
			_, err := create(t, func(s *computev1alpha.InstanceConsoleSession) {
				s.Spec.ClientPublicKey = key
			})
			require.Error(t, err)
		})
	}

	t.Run("a session must name the instance's UID", func(t *testing.T) {
		_, err := create(t, func(s *computev1alpha.InstanceConsoleSession) {
			s.Spec.InstanceRef.UID = ""
		})
		require.Error(t, err)
	})

	t.Run("a container name is at most 63 characters", func(t *testing.T) {
		_, err := create(t, func(s *computev1alpha.InstanceConsoleSession) {
			s.Spec.ContainerName = strings.Repeat("c", 64)
		})
		require.Error(t, err)
	})
}

func newConsoleSession() *computev1alpha.InstanceConsoleSession {
	return &computev1alpha.InstanceConsoleSession{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "web-0-",
			Namespace:    "default",
		},
		Spec: computev1alpha.InstanceConsoleSessionSpec{
			InstanceRef: computev1alpha.InstanceConsoleSessionInstanceRef{
				Name: "web-0",
				UID:  "3d39d44d-93fb-4d78-a14a-d76a9876aa71",
			},
			ContainerName:   "app",
			Command:         []string{"sh"},
			Stdin:           true,
			Terminal:        true,
			ClientPublicKey: strings.Repeat("0123456789abcdef", 4),
		},
	}
}
