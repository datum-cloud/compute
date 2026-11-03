// SPDX-License-Identifier: AGPL-3.0-only

// Package consolesession holds the connection protocol shared by the shell
// agent and its clients: how a client proves it holds the key named in an
// InstanceConsoleSession, and the constants both sides must agree on.
package consolesession

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"time"
)

const (
	// ALPN is the iroh protocol the tunnel endpoint serves.
	ALPN = "iroh-http-proxy/1"

	// SubProtocol is the WebSocket subprotocol of the exec stream.
	SubProtocol = "v5.channel.k8s.io"

	// HeaderTimestamp carries the signing time in Unix seconds.
	HeaderTimestamp = "X-Datum-Timestamp"

	// HeaderSignature carries the base64 ed25519 signature.
	HeaderSignature = "X-Datum-Signature"

	// ClockSkew is how far a signing time may differ from the agent's clock.
	ClockSkew = 30 * time.Second

	signaturePrefix = "datum-exec-v1"
)

// ExecPath is the agent's exec endpoint for a session, keyed by the project
// session's UID.
func ExecPath(sessionUID string) string {
	return "/v1/sessions/" + sessionUID + "/exec"
}

// PublicKey returns key's public half in the form spec.clientPublicKey takes:
// 64 lowercase hexadecimal characters, which is also its iroh endpoint ID.
func PublicKey(key ed25519.PrivateKey) string {
	return hex.EncodeToString(key.Public().(ed25519.PublicKey))
}

// Sign returns the timestamp and signature headers that let the holder of key
// connect to the session with the given UID.
func Sign(key ed25519.PrivateKey, sessionUID string, now time.Time) (timestamp, signature string) {
	ts := now.Unix()
	sig := ed25519.Sign(key, message(sessionUID, ts))
	return strconv.FormatInt(ts, 10), base64.StdEncoding.EncodeToString(sig)
}

// Verify checks that timestamp and signature were produced by the key named in
// publicKey for the session with the given UID, within skew of now.
func Verify(publicKey, sessionUID, timestamp, signature string, now time.Time, skew time.Duration) error {
	pub, err := hex.DecodeString(publicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("session names an invalid client public key")
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errors.New("missing or malformed timestamp")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > skew || d < -skew {
		return fmt.Errorf("timestamp outside the allowed %s skew", skew)
	}
	sig, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return errors.New("malformed signature")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), message(sessionUID, ts), sig) {
		return errors.New("signature does not match the session's client key")
	}
	return nil
}

func message(sessionUID string, ts int64) []byte {
	return fmt.Appendf(nil, "%s\n%s\n%d", signaturePrefix, sessionUID, ts)
}
