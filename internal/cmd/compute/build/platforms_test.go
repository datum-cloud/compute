// SPDX-License-Identifier: AGPL-3.0-only

package build

import (
	"context"
	"net/http/httptest"
	"net/url"
	"slices"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const platformLinux = "linux/amd64"

func TestImagePlatforms(t *testing.T) {
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	host := mustHost(t, srv.URL)

	linux := platformImage(t, "linux", "amd64")
	unlabeled, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	compute, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	idx := mutate.AppendManifests(empty.Index,
		mutate.IndexAddendum{Add: linux, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: goosLinux, Architecture: "amd64"}}},
		mutate.IndexAddendum{Add: compute, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: computePlatformOS, Architecture: computePlatformArch}}},
		mutate.IndexAddendum{Add: compute, Descriptor: v1.Descriptor{Platform: &v1.Platform{OS: unknownPlatformLabel, Architecture: unknownPlatformLabel}}},
	)
	indexRef := host + "/acme/api:multi"
	if err := remote.WriteIndex(mustRef(t, indexRef), idx); err != nil {
		t.Fatal(err)
	}
	singleRef := host + "/acme/api:single"
	if err := remote.Write(mustRef(t, singleRef), linux); err != nil {
		t.Fatal(err)
	}

	got, err := ImagePlatforms(context.Background(), indexRef)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{platformLinux, ComputePlatform} {
		if !slices.Contains(got, want) {
			t.Errorf("index platforms %v lack %s", got, want)
		}
	}
	if len(got) != 2 {
		t.Errorf("index platforms %v should leave out the attestation manifest", got)
	}

	got, err = ImagePlatforms(context.Background(), singleRef)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []string{platformLinux}) {
		t.Errorf("single manifest platforms = %v, want [%s]", got, platformLinux)
	}

	unlabeledRef := host + "/acme/api:unlabeled"
	if err := remote.Write(mustRef(t, unlabeledRef), unlabeled); err != nil {
		t.Fatal(err)
	}
	got, err = ImagePlatforms(context.Background(), unlabeledRef)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("an image with no platform in its config reported %v", got)
	}

	if _, err := ImagePlatforms(context.Background(), host+"/acme/api:missing"); err == nil {
		t.Error("a missing tag must return an error")
	}
}

func TestIsComputePlatform(t *testing.T) {
	for p, want := range map[string]bool{ComputePlatform: true, "kraftcloud/x86_64": true, platformLinux: false, "": false, computePlatformOS: false} {
		if got := IsComputePlatform(p); got != want {
			t.Errorf("IsComputePlatform(%q) = %v, want %v", p, got, want)
		}
	}
}

func mustHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Host
}

func mustRef(t *testing.T, s string) name.Reference {
	t.Helper()
	ref, err := name.ParseReference(s)
	if err != nil {
		t.Fatal(err)
	}
	return ref
}

func platformImage(t *testing.T, os, arch string) v1.Image {
	t.Helper()
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		t.Fatal(err)
	}
	cfg = cfg.DeepCopy()
	cfg.OS, cfg.Architecture = os, arch
	img, err = mutate.ConfigFile(img, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return img
}
