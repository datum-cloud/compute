// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"crypto/x509"
	"testing"

	"golang.org/x/crypto/x509roots/fallback/bundle"
)

var distrustedRoots = []string{
	"emSign ECC Root CA - G3",
}

func TestFallbackBundleOmitsDistrustedRoots(t *testing.T) {
	trusted := map[string]bool{}
	count := 0
	for root := range bundle.Roots() {
		cert, err := x509.ParseCertificate(root.Certificate)
		if err != nil {
			t.Fatalf("parse fallback root: %v", err)
		}
		trusted[cert.Subject.CommonName] = true
		count++
	}
	if count == 0 {
		t.Fatal("fallback bundle yielded no roots")
	}
	for _, cn := range distrustedRoots {
		if trusted[cn] {
			t.Errorf("fallback bundle trusts distrusted root %q", cn)
		}
	}
}
