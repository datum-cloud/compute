package build

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
)

const testImageRef = "ghcr.io/acme/api:latest"

func testComputeImage(t *testing.T) v1.Image {
	t.Helper()
	dir := t.TempDir()
	initrd := filepath.Join(dir, "rootfs.erofs")
	if err := os.WriteFile(initrd, []byte("erofs"), 0o644); err != nil {
		t.Fatal(err)
	}
	img, err := assembleComputeImage(dir, packagingArtifact{
		Path:   initrd,
		Config: imageConfig{Args: []string{"/app"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return img
}

func TestInspectReadsBuildOutputs(t *testing.T) {
	img := testComputeImage(t)
	wantDigest, err := computeImageIndex(img).Digest()
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	outputs := map[string]func(string) error{
		"compute-image.tar":    func(p string) error { return exportArchive(p, img) },
		"compute-image.tar.gz": func(p string) error { return exportArchive(p, img) },
		"compute-image":        func(p string) error { return exportLayout(p, img) },
	}
	for name, write := range outputs {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, name)
			if err := write(path); err != nil {
				t.Fatal(err)
			}
			idx, cleanup, err := openLocalImage(path)
			if err != nil {
				t.Fatal(err)
			}
			if idx == nil {
				t.Fatal("expected a local image")
				return
			}
			defer cleanup()

			got := imageInspection{}
			if err := inspectIndex(&got, idx); err != nil {
				t.Fatal(err)
			}
			if got.IndexDigest != wantDigest.String() {
				t.Errorf("digest = %s, want %s (the digest a push would report)", got.IndexDigest, wantDigest)
			}
			if got.SelectedPlatform != "datum/x86_64" || !got.RootFS.Found || len(got.Issues) > 0 {
				t.Errorf("unexpected inspection: %+v", got)
			}
		})
	}
}

func TestInspectSelectsPreferredPlatform(t *testing.T) {
	img := testComputeImage(t)
	kraftcloud := v1.Platform{OS: "kraftcloud", Architecture: computePlatformArch}
	tests := []struct {
		name       string
		platforms  []v1.Platform
		want       string
		wantIssues bool
	}{
		{
			name:      "image built before the switch to datum",
			platforms: []v1.Platform{kraftcloud},
			want:      kraftcloud.String(),
		},
		{
			name:      "datum preferred over kraftcloud",
			platforms: []v1.Platform{kraftcloud, {OS: "datum", Architecture: computePlatformArch}},
			want:      "datum/x86_64",
		},
		{
			name:       "wrong architecture",
			platforms:  []v1.Platform{{OS: kraftcloud.OS, Architecture: "arm64"}},
			want:       kraftcloud.OS + "/arm64",
			wantIssues: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adds := make([]mutate.IndexAddendum, 0, len(tt.platforms))
			for _, platform := range tt.platforms {
				adds = append(adds, mutate.IndexAddendum{
					Add:        img,
					Descriptor: v1.Descriptor{Platform: &platform},
				})
			}
			got := imageInspection{}
			if err := inspectIndex(&got, mutate.AppendManifests(empty.Index, adds...)); err != nil {
				t.Fatal(err)
			}
			if got.SelectedPlatform != tt.want || (len(got.Issues) > 0) != tt.wantIssues {
				t.Errorf("unexpected inspection: %+v", got)
			}
		})
	}
}

func TestInspectLocalPathErrors(t *testing.T) {
	dir := t.TempDir()
	notImage := filepath.Join(dir, "notes")
	if err := os.Mkdir(notImage, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		arg  string
		want string
	}{
		{filepath.Join(dir, "missing.tar"), "missing.tar doesn't exist."},
		{"./definitely-missing-dir", "./definitely-missing-dir doesn't exist."},
		{notImage, "notes isn't an image saved with build --output."},
	}
	for _, tt := range tests {
		_, _, err := openLocalImage(tt.arg)
		var ue *userError
		if !errors.As(err, &ue) || !strings.Contains(ue.Error(), tt.want) {
			t.Errorf("openLocalImage(%q) error = %v, want it to contain %q", tt.arg, err, tt.want)
		}
	}
}

func TestInspectRegistryReferencesStayRemote(t *testing.T) {
	// A bare name stays a registry reference even when a folder of that
	// name exists, matching how --output reads it.
	t.Chdir(t.TempDir())
	if err := os.Mkdir("nginx", 0o755); err != nil {
		t.Fatal(err)
	}
	for _, arg := range []string{testImageRef, "acme/api", "nginx"} {
		idx, _, err := openLocalImage(arg)
		if err != nil || idx != nil {
			t.Errorf("openLocalImage(%q) = %v, %v; want it treated as a registry reference", arg, idx, err)
		}
	}
}
