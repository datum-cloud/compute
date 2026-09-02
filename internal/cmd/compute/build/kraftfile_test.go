//nolint:goconst // table-driven test cases intentionally repeat literals
package build

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindKraftfileLocatesDefaultName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Kraftfile")
	if err := os.WriteFile(path, []byte("spec: v0.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := FindKraftfile(dir); got != path {
		t.Fatalf("expected %q, got %q", path, got)
	}
}

func TestFindKraftfileReturnsEmptyWhenAbsent(t *testing.T) {
	if got := FindKraftfile(t.TempDir()); got != "" {
		t.Fatalf("expected no Kraftfile, got %q", got)
	}
}

func TestRunKraftBuildPromptsToInstallWhenMissing(t *testing.T) {
	// Force exec.LookPath("unikraft") to fail regardless of the host environment.
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	kraftfilePath := filepath.Join(dir, "Kraftfile")
	if err := os.WriteFile(kraftfilePath, []byte("spec: v0.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := &Options{ContextDir: dir, Kraftfile: kraftfilePath}

	err := runKraftBuild(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error when unikraft is not installed")
	}
	if !strings.Contains(err.Error(), "unikraft CLI is not installed") {
		t.Fatalf("expected an install prompt, got: %v", err)
	}
}

// TestRunKraftBuildRejectsNonStandardKraftfileName documents a real
// limitation of the unikraft CLI: unikraft build takes an input directory
// and auto-discovers a Kraftfile within it (see kraftfileNames), it has no
// flag to point at an arbitrarily-named or arbitrarily-located file.
func TestRunKraftBuildRejectsNonStandardKraftfileName(t *testing.T) {
	dir := t.TempDir()
	kraftfilePath := filepath.Join(dir, "my-custom-kraftfile.yaml")
	if err := os.WriteFile(kraftfilePath, []byte("spec: v0.7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := &Options{ContextDir: dir, Kraftfile: kraftfilePath}

	err := runKraftBuild(context.Background(), opts)
	if err == nil {
		t.Fatal("expected an error for a non-standard Kraftfile name")
	}
	if !strings.Contains(err.Error(), "won't auto-discover") {
		t.Fatalf("expected a discovery-limitation error, got: %v", err)
	}
}

func TestFormatCommandQuotesOnlyWhenNeeded(t *testing.T) {
	got := formatCommand("/usr/local/bin/unikraft", []string{
		"build", "/src/app",
		"--build-arg", "VERSION=1.2.3",
		"--build-arg", "MESSAGE=hello world",
		"--output", "ghcr.io/acme/api:latest",
	})
	want := "/usr/local/bin/unikraft build /src/app --build-arg VERSION=1.2.3 --build-arg 'MESSAGE=hello world' --output ghcr.io/acme/api:latest"
	if got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}
