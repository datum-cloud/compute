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
	if !strings.Contains(err.Error(), "building from a Kraftfile needs the unikraft CLI, which isn't installed.") {
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

const testDockerfile = "Dockerfile"

func writeFiles(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("FROM scratch\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunDoesNotDiscoverKraftfile(t *testing.T) {
	// With unikraft off PATH, a handoff would fail with "needs the unikraft
	// CLI"; reaching BuildKit instead proves the Dockerfile was built.
	t.Setenv("PATH", t.TempDir())
	t.Setenv("BUILDKIT_HOST", "unix://"+filepath.Join(shortTempDir(t), "missing.sock"))
	dir := t.TempDir()
	writeFiles(t, dir, testDockerfile, "Kraftfile")

	_, err := Run(context.Background(), &Options{ContextDir: dir, Dockerfile: testDockerfile})
	if err == nil || !strings.Contains(err.Error(), "can't reach BuildKit") {
		t.Fatalf("expected the Dockerfile build to reach BuildKit, got: %v", err)
	}
}

func TestRunMissingDockerfile(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	tests := []struct {
		name     string
		files    []string
		explicit bool
		want     []string
	}{
		{
			name:  "Kraftfile only",
			files: []string{"Kraftfile"},
			want:  []string{"there's no Dockerfile in ", "To build from the Kraftfile there, add --kraftfile "},
		},
		{
			name: "empty folder",
			want: []string{"there's no Dockerfile in ", "Add a Dockerfile, or use -f to point to one."},
		},
		{
			name:     "explicit -f",
			explicit: true,
			want:     []string{"there's no Dockerfile at ", "Check the path you passed to -f."},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFiles(t, dir, tt.files...)
			_, err := Run(context.Background(), &Options{ContextDir: dir, Dockerfile: testDockerfile, DockerfileExplicit: tt.explicit})
			if err == nil {
				t.Fatal("expected an error")
			}
			assertContains(t, err.Error(), tt.want...)
		})
	}
}

func TestRunMissingDockerfileInCurrentFolder(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, "Kraftfile")
	t.Chdir(dir)

	_, err := Run(context.Background(), &Options{ContextDir: ".", Dockerfile: testDockerfile})
	if err == nil {
		t.Fatal("expected an error")
	}
	assertContains(t, err.Error(),
		"there's no Dockerfile in this folder.\n\nTo build from the Kraftfile here, add --kraftfile Kraftfile.")
}

func TestRunRejectsKraftfileWithFile(t *testing.T) {
	dir := t.TempDir()
	writeFiles(t, dir, testDockerfile, "Kraftfile")
	_, err := Run(context.Background(), &Options{
		ContextDir:         dir,
		Dockerfile:         testDockerfile,
		DockerfileExplicit: true,
		Kraftfile:          filepath.Join(dir, "Kraftfile"),
	})
	if err == nil || !strings.Contains(err.Error(), "--kraftfile and --file can't be used together.") {
		t.Fatalf("expected a conflicting flags error, got: %v", err)
	}
}

func TestHasDockerfile(t *testing.T) {
	for name, files := range map[string][]string{
		testDockerfile:     {testDockerfile},
		"Dockerfile.datum": {"Dockerfile.datum"},
		"none":             {"Kraftfile"},
	} {
		dir := t.TempDir()
		writeFiles(t, dir, files...)
		if got, want := HasDockerfile(dir, testDockerfile), name != "none"; got != want {
			t.Errorf("%s: HasDockerfile = %v, want %v", name, got, want)
		}
	}
}
