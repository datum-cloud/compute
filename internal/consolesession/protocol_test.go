// SPDX-License-Identifier: AGPL-3.0-only

package consolesession

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

const sessionUID = "uid-1"

func TestVerify(t *testing.T) {
	key, other := newKey(t), newKey(t)
	now := time.Unix(1_800_000_000, 0)
	ts, sig := Sign(key, sessionUID, now)

	cases := []struct {
		name      string
		publicKey string
		uid       string
		at        time.Time
		wantErr   bool
	}{
		{"valid", PublicKey(key), sessionUID, now, false},
		{"within skew", PublicKey(key), sessionUID, now.Add(ClockSkew), false},
		{"expired timestamp", PublicKey(key), sessionUID, now.Add(ClockSkew + time.Second), true},
		{"future timestamp", PublicKey(key), sessionUID, now.Add(-ClockSkew - time.Second), true},
		{"other client key", PublicKey(other), sessionUID, now, true},
		{"other session", PublicKey(key), "uid-2", now, true},
		{"malformed key", "not-hex", sessionUID, now, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := Verify(tc.publicKey, tc.uid, ts, sig, tc.at, ClockSkew)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Verify() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestPublicKeyMatchesAPIFormat(t *testing.T) {
	got := PublicKey(newKey(t))
	if len(got) != 64 {
		t.Fatalf("PublicKey() length = %d, want 64", len(got))
	}
	for _, c := range got {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			t.Fatalf("PublicKey() = %q, want lowercase hexadecimal", got)
		}
	}
}

func TestExecPath(t *testing.T) {
	if got, want := ExecPath(sessionUID), "/v1/sessions/uid-1/exec"; got != want {
		t.Fatalf("ExecPath() = %q, want %q", got, want)
	}
}
