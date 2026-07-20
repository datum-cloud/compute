//nolint:goconst // table-driven test cases intentionally repeat literals
package build

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func writeTestTar(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for name, content := range entries {
		if err := w.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(content)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "archive.tar")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestUntarExtractsRegularFiles(t *testing.T) {
	archivePath := writeTestTar(t, map[string]string{"app/server": "binary-contents"})
	dest := t.TempDir()
	if err := untar(archivePath, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "app", "server"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "binary-contents" {
		t.Fatalf("expected binary-contents, got %q", got)
	}
}

// TestUntarRejectsPathTraversal is a regression test for the path-traversal
// guard: it must use POSIX semantics (tar entry names are always
// forward-slash), not filepath.Clean/IsAbs, which are OS-dependent and on
// Windows would fail to reject a POSIX-absolute path like "/etc/passwd".
func TestUntarRejectsPathTraversal(t *testing.T) {
	for _, name := range []string{"../escape", "a/../../escape", "/etc/passwd"} {
		archivePath := writeTestTar(t, map[string]string{name: "x"})
		dest := t.TempDir()
		if err := untar(archivePath, dest); err == nil {
			t.Fatalf("expected unsafe path error for %q, got nil", name)
		}
	}
}

// TestOpenTarRootFSRecordsExplicitDirectories is a regression test:
// openTarFSView must record tar.TypeDir entries into Dirs, not just infer
// directories from file ancestors, so a real but empty directory (e.g. a
// mount point) is visible to callers like the sandboxed script tracer.
func TestOpenTarRootFSRecordsExplicitDirectories(t *testing.T) {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: "data", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(t.TempDir(), "archive.tar")
	if err := os.WriteFile(archivePath, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}

	view, err := openTarFSView(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := view.Dirs["data"]; !ok {
		t.Fatalf("expected \"data\" in Dirs, got %#v", view.Dirs)
	}
}

func TestParseOutput(t *testing.T) {
	tests := []struct {
		name string
		in   string
		kind outputKind
	}{
		{name: "empty debug", in: "", kind: outputDebug},
		{name: "archive", in: "image.tar", kind: outputArchive},
		{name: "gzip archive", in: "image.tar.gz", kind: outputArchive},
		{name: "explicit relative layout", in: "./image", kind: outputLayout},
		{name: "parent relative layout", in: "../image", kind: outputLayout},
		{name: "absolute layout", in: "/tmp/image", kind: outputLayout},
		{name: "home layout", in: "~/image", kind: outputLayout},
		{name: "registry", in: "ghcr.io/acme/api:dev", kind: outputRegistry},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseOutput(tt.in)
			if got.kind != tt.kind {
				t.Fatalf("parseOutput(%q).kind = %v, want %v", tt.in, got.kind, tt.kind)
			}
			if got.value != tt.in {
				t.Fatalf("parseOutput(%q).value = %q, want %q", tt.in, got.value, tt.in)
			}
		})
	}
}

func TestValidateOutputOptionsRequiresRegistryForPush(t *testing.T) {
	if err := validateOutputOptions(&options{push: true}, outputSpec{kind: outputRegistry, value: "ghcr.io/acme/api:dev"}); err != nil {
		t.Fatalf("registry output with --push returned error: %v", err)
	}

	if err := validateOutputOptions(&options{push: true}, outputSpec{kind: outputArchive, value: "image.tar"}); err == nil {
		t.Fatal("expected local archive output with --push to fail")
	}

	if err := validateOutputOptions(&options{push: true}, outputSpec{kind: outputDebug}); err == nil {
		t.Fatal("expected debug output with --push to fail")
	}
}

func TestResolveDockerfilePathPrefersDatumOverride(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.datum"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveDockerfilePath(dir, "Dockerfile", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "Dockerfile.datum") {
		t.Fatalf("expected Dockerfile.datum, got %q", got)
	}
}

func TestResolveDockerfilePathHonorsExplicitFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile.datum"), []byte("FROM scratch\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := resolveDockerfilePath(dir, "Dockerfile", true)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(dir, "Dockerfile") {
		t.Fatalf("expected explicit Dockerfile, got %q", got)
	}
}

func TestApplyExactLineFixes(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		findings    []finding
		wantApplied int
		want        string
	}{
		{name: "multiline edit appends after the line",
			content:     "FROM scratch\nCOPY app /app\n",
			findings:    []finding{{Edits: []sourceEdit{{Line: 2, Old: "COPY app /app", New: "COPY app /app\nCOPY libc.so.6 /lib/libc.so.6"}}}},
			wantApplied: 1,
			want:        "FROM scratch\nCOPY app /app\nCOPY libc.so.6 /lib/libc.so.6\n"},
		{name: "single-line replace",
			content:     "FROM scratch\nRUN go build -o /app .\n",
			findings:    []finding{{Edits: []sourceEdit{{Line: 2, Old: "RUN go build -o /app .", New: "RUN go build -trimpath -o /app ."}}}},
			wantApplied: 1,
			want:        "FROM scratch\nRUN go build -trimpath -o /app .\n"},
		// Two findings target the same original line: only the first can
		// apply — applyExactLineEdit re-reads the file and matches edit.Old
		// against current content, so the second no-ops rather than erroring
		// or double-applying.
		{name: "second edit on the same line no-ops",
			content: "FROM scratch\nCOPY --from=build /start.sh /start.sh\n",
			findings: []finding{
				{Edits: []sourceEdit{{Line: 2, Old: "COPY --from=build /start.sh /start.sh", New: "COPY --chmod=755 --from=build /start.sh /start.sh"}}},
				{Edits: []sourceEdit{{Line: 2, Old: "COPY --from=build /start.sh /start.sh", New: "COPY --from=build /start.sh /start.sh\nCOPY --from=build /lib/libc.so.6 /lib/libc.so.6"}}},
			},
			wantApplied: 1,
			want:        "FROM scratch\nCOPY --chmod=755 --from=build /start.sh /start.sh\n"},
		{name: "earlier multi-line edit doesn't shift a later edit's line match",
			content: "FROM scratch\nCOPY --from=build /app /app\nCOPY start.sh /start.sh\n",
			findings: []finding{
				{Edits: []sourceEdit{{Line: 2, Old: "COPY --from=build /app /app", New: "COPY --from=build /app /app\nCOPY --from=build /lib/libc.so.6 /lib/libc.so.6"}}},
				{Edits: []sourceEdit{{Line: 3, Old: "COPY start.sh /start.sh", New: "COPY --chmod=755 start.sh /start.sh"}}},
			},
			wantApplied: 2,
			want:        "FROM scratch\nCOPY --from=build /app /app\nCOPY --from=build /lib/libc.so.6 /lib/libc.so.6\nCOPY --chmod=755 start.sh /start.sh\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "Dockerfile")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatal(err)
			}
			for i := range tt.findings {
				for j := range tt.findings[i].Edits {
					tt.findings[i].Edits[j].File = path
				}
			}

			applied, err := applyExactLineFixes(&analysisResult{Findings: tt.findings})
			if err != nil {
				t.Fatal(err)
			}
			if applied != tt.wantApplied {
				t.Fatalf("expected %d applied fixes, got %d", tt.wantApplied, applied)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(data) != tt.want {
				t.Fatalf("expected %q, got %q", tt.want, string(data))
			}
		})
	}
}

func TestAssembleImageUsesComputeIndexAndInitrdAnnotation(t *testing.T) {
	tmpDir := t.TempDir()
	initrdPath := filepath.Join(tmpDir, "initramfs.erofs")
	if err := os.WriteFile(initrdPath, []byte("erofs"), 0o644); err != nil {
		t.Fatal(err)
	}

	img, err := assembleComputeImage(tmpDir, packagingArtifact{
		Path:   initrdPath,
		Config: imageConfig{},
	})
	if err != nil {
		t.Fatal(err)
	}

	manifest, err := img.Manifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Layers) != 1 {
		t.Fatalf("expected one layer, got %d", len(manifest.Layers))
	}
	if got := manifest.Layers[0].MediaType; got != "application/vnd.oci.image.layer.v1.tar" {
		t.Fatalf("expected uncompressed OCI layer media type, got %q", got)
	}
	if got := manifest.Layers[0].Annotations[annotationInitrdPath]; got != "/unikraft/bin/initrd" {
		t.Fatalf("expected initrd annotation, got %q", got)
	}

	idxManifest, err := computeImageIndex(img).IndexManifest()
	if err != nil {
		t.Fatal(err)
	}
	if len(idxManifest.Manifests) != 1 {
		t.Fatalf("expected one index manifest, got %d", len(idxManifest.Manifests))
	}
	platform := idxManifest.Manifests[0].Platform
	if platform == nil || platform.OS != "kraftcloud" || platform.Architecture != "x86_64" {
		t.Fatalf("expected kraftcloud/x86_64 platform, got %#v", platform)
	}
}
