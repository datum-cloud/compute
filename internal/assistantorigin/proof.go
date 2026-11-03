package assistantorigin

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	computev1alpha "go.datum.net/compute/api/v1alpha"
)

// KeyEnv is shared by the MCP server and compute controller. Its value is a
// base64 key provisioned by the operator, never passed in tool arguments.
const KeyEnv = "COMPUTE_ASSISTANT_EXEC_KEY"

func KeyFromEnv() ([]byte, error) {
	encoded := os.Getenv(KeyEnv)
	if encoded == "" {
		return nil, nil
	}
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		key, err = base64.RawStdEncoding.DecodeString(encoded)
	}
	if err != nil || len(key) < 32 {
		return nil, errors.New(KeyEnv + " must be at least 32 base64-encoded bytes")
	}
	return key, nil
}

// Sign binds the creator and complete immutable spec, including the one-time
// client public key, to this assistant operation. The value is safe to store
// as an annotation; without the key a client cannot produce a new valid one.
func Sign(key []byte, session *computev1alpha.InstanceConsoleSession, requester, nonce string, expiresAt time.Time) (string, error) {
	if len(key) < 32 || requester == "" || nonce == "" {
		return "", errors.New("assistant origin is not configured")
	}
	expiry := strconv.FormatInt(expiresAt.Unix(), 10)
	sig, err := signature(key, session, requester, expiry, nonce)
	if err != nil {
		return "", err
	}
	return expiry + "." + nonce + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

// Verify checks a proof at creation time. It rejects expired assertions and
// every changed field of the session spec, including argv, TTL, and public key.
func Verify(key []byte, session *computev1alpha.InstanceConsoleSession, requester, proof string, now time.Time) bool {
	expiry, _, _, ok := split(proof)
	if !ok {
		return false
	}
	until, err := strconv.ParseInt(expiry, 10, 64)
	return err == nil && until > now.Unix() && until <= now.Add(5*time.Minute).Unix() &&
		VerifyStored(key, session, requester, proof)
}

// VerifyStored checks a previously admitted session for activity attribution.
// Expiry is omitted because a valid command may outlive the short admission
// window; the admission webhook already rejected expired proofs at creation.
func VerifyStored(key []byte, session *computev1alpha.InstanceConsoleSession, requester, proof string) bool {
	if len(key) < 32 || requester == "" {
		return false
	}
	expiry, nonce, encoded, ok := split(proof)
	if !ok {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	want, err := signature(key, session, requester, expiry, nonce)
	return err == nil && hmac.Equal(got, want)
}

func split(proof string) (string, string, string, bool) {
	parts := strings.Split(proof, ".")
	if len(parts) != 3 || parts[0] == "" || len(parts[1]) != 32 || parts[2] == "" {
		return "", "", "", false
	}
	for _, ch := range parts[1] {
		if ch < '0' || (ch > '9' && ch < 'a') || ch > 'f' {
			return "", "", "", false
		}
	}
	return parts[0], parts[1], parts[2], true
}

func signature(key []byte, session *computev1alpha.InstanceConsoleSession, requester, expiry, nonce string) ([]byte, error) {
	payload, err := json.Marshal(struct {
		Name      string                                    `json:"name"`
		Namespace string                                    `json:"namespace"`
		Requester string                                    `json:"requester"`
		Spec      computev1alpha.InstanceConsoleSessionSpec `json:"spec"`
		Expiry    string                                    `json:"expiry"`
		Nonce     string                                    `json:"nonce"`
	}{session.Name, session.Namespace, requester, session.Spec, expiry, nonce})
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("compute-assistant-origin-v1\x00"))
	_, _ = mac.Write(payload)
	return mac.Sum(nil), nil
}
