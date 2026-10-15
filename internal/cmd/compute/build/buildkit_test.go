package build

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func TestNativeBuildSolveOpt(t *testing.T) {
	dir := t.TempDir()
	dockerfile := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VERSION", "1.2.3")

	opt, err := buildSolveOpt(buildRequest{
		ContextDir: dir,
		Dockerfile: dockerfile,
		Target:     "production",
		BuildArgs:  []string{"VERSION", "MODE=release"},
	}, nopWriteCloser{io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if opt.Frontend != "dockerfile.v0" {
		t.Fatalf("expected dockerfile frontend, got %q", opt.Frontend)
	}
	if opt.FrontendAttrs["filename"] != "Dockerfile" {
		t.Fatalf("unexpected filename attr: %#v", opt.FrontendAttrs)
	}
	if opt.FrontendAttrs["platform"] != "linux/amd64" {
		t.Fatalf("unexpected platform attr: %#v", opt.FrontendAttrs)
	}
	if opt.FrontendAttrs["target"] != "production" {
		t.Fatalf("unexpected target attr: %#v", opt.FrontendAttrs)
	}
	if opt.FrontendAttrs["build-arg:VERSION"] != "1.2.3" {
		t.Fatalf("expected inherited build arg, got %#v", opt.FrontendAttrs)
	}
	if opt.FrontendAttrs["build-arg:MODE"] != "release" {
		t.Fatalf("expected explicit build arg, got %#v", opt.FrontendAttrs)
	}
	if len(opt.Session) == 0 {
		t.Fatal("expected auth session attachable")
	}
}
