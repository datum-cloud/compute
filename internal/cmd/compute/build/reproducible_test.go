package build

import (
	"archive/tar"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeRootfsTar(t *testing.T, modTime time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rootfs.tar")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(f)
	for _, hdr := range []*tar.Header{
		{Name: "app/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: modTime},
		{Name: "app/main", Typeflag: tar.TypeReg, Mode: 0o755, Size: 5, ModTime: modTime},
	} {
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if hdr.Size > 0 {
			if _, err := io.WriteString(tw, "hello"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func erofsBytes(t *testing.T, tarPath string, epoch *time.Time) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "rootfs.erofs")
	if err := createErofsFromTar(tarPath, out, epoch); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestErofsIsByteIdenticalAcrossRuns(t *testing.T) {
	tarPath := writeRootfsTar(t, time.Unix(1_700_000_000, 0))
	first := erofsBytes(t, tarPath, nil)
	// The EROFS writer used to stamp the current second into the image.
	time.Sleep(1100 * time.Millisecond)
	if !bytes.Equal(first, erofsBytes(t, tarPath, nil)) {
		t.Fatal("packaging the same rootfs twice gave different images")
	}
}

func TestErofsClampsFileTimesToSourceDateEpoch(t *testing.T) {
	epoch := time.Unix(1_600_000_000, 0)
	coldBuild := writeRootfsTar(t, time.Unix(1_700_000_000, 0))
	laterBuild := writeRootfsTar(t, time.Unix(1_700_000_500, 0))

	if bytes.Equal(erofsBytes(t, coldBuild, nil), erofsBytes(t, laterBuild, nil)) {
		t.Fatal("without SOURCE_DATE_EPOCH, file times should still be kept")
	}
	if !bytes.Equal(erofsBytes(t, coldBuild, &epoch), erofsBytes(t, laterBuild, &epoch)) {
		t.Fatal("with SOURCE_DATE_EPOCH, builds that differ only in file times should match")
	}
}

func TestParseSourceDateEpoch(t *testing.T) {
	t.Setenv(sourceDateEpochEnv, "")
	if got, err := parseSourceDateEpoch(); got != nil || err != nil {
		t.Fatalf("unset: got %v, %v", got, err)
	}

	t.Setenv(sourceDateEpochEnv, "1700000000")
	got, err := parseSourceDateEpoch()
	if err != nil || got == nil || got.Unix() != 1_700_000_000 {
		t.Fatalf("valid: got %v, %v", got, err)
	}

	for _, bad := range []string{"yesterday", "-5", "1.5"} {
		t.Setenv(sourceDateEpochEnv, bad)
		if _, err := parseSourceDateEpoch(); err == nil {
			t.Errorf("%q: expected an error", bad)
		} else {
			assertContains(t, err.Error(), "isn't a valid timestamp", "seconds since 1970")
		}
	}
}

func TestSolveOptPassesSourceDateEpoch(t *testing.T) {
	dir := t.TempDir()
	dockerfile := filepath.Join(dir, "Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(sourceDateEpochEnv, "1700000000")

	for _, tt := range []struct {
		buildArgs []string
		want      string
	}{
		{nil, "1700000000"},
		{[]string{"SOURCE_DATE_EPOCH=1"}, "1"},
	} {
		opt, err := buildSolveOpt(buildRequest{ContextDir: dir, Dockerfile: dockerfile, BuildArgs: tt.buildArgs}, nopWriteCloser{io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		if got := opt.FrontendAttrs["build-arg:SOURCE_DATE_EPOCH"]; got != tt.want {
			t.Errorf("build args %v: SOURCE_DATE_EPOCH = %q, want %q", tt.buildArgs, got, tt.want)
		}
	}
}
