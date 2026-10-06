package build

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"golang.org/x/term"
)

type outputKind int

const (
	outputDebug outputKind = iota
	outputRegistry
	outputArchive
	outputLayout
)

type outputSpec struct {
	kind  outputKind
	value string
}

// handleOutput dispatches the built image to its destination and returns the
// pushed image pinned by digest (repo@sha256:...) when the destination is a
// registry, or "" for archive/layout/debug outputs.
func handleOutput(ctx context.Context, opts *Options, spec outputSpec, img v1.Image) (string, error) {
	switch spec.kind {
	case outputDebug:
		if opts.Push {
			return "", fmt.Errorf("--push requires a registry output: use --output ghcr.io/acme/api:tag")
		}
		name := imageNameFor(opts.ContextDir)
		fmt.Fprintf(os.Stderr, "Preview complete. The image wasn't saved: add --output ./%s.tar to save it,\n"+
			"or --output <registry>/<account>/%s:latest to push it.\n", name, name)
		return "", nil
	case outputRegistry:
		if !opts.Push {
			ok, err := confirmRegistryPush(spec.value)
			if err != nil {
				return "", err
			}
			if !ok {
				return "", fmt.Errorf("push cancelled")
			}
		}
		return pushImage(ctx, opts, img)
	case outputArchive:
		if opts.Push {
			return "", fmt.Errorf("--push is only valid with registry outputs")
		}
		return "", exportArchive(spec.value, img)
	case outputLayout:
		if opts.Push {
			return "", fmt.Errorf("--push is only valid with registry outputs")
		}
		return "", exportLayout(spec.value, img)
	default:
		return "", fmt.Errorf("unknown output type")
	}
}

var invalidImageNameChars = regexp.MustCompile(`[^a-z0-9]+`)

// imageNameFor suggests an image name from the build folder's name, made
// valid for a registry reference.
func imageNameFor(contextDir string) string {
	name := invalidImageNameChars.ReplaceAllString(strings.ToLower(filepath.Base(contextDir)), "-")
	name = strings.Trim(name, "-")
	if name == "" {
		return "app"
	}
	return name
}

// pushImage pushes img to opts.Ref and returns the pushed image pinned by
// digest (repo@sha256:...), since a tag is mutable but callers that chain
// into a deploy step need to pin what they just pushed.
func pushImage(ctx context.Context, opts *Options, img v1.Image) (string, error) {
	task := stderrSpinner.Start("Pushing " + opts.Ref)
	ref, err := name.ParseReference(opts.Ref)
	if err != nil {
		task.Done(err)
		return "", err
	}
	updates := make(chan v1.Update, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for update := range updates {
			if update.Total > 0 {
				msg := fmt.Sprintf("%s / %s", formatMB(update.Complete), formatMB(update.Total))
				if update.Complete >= update.Total {
					msg += " (finalizing)"
				}
				task.Update(msg)
			}
		}
	}()
	// Push the Compute index, not the bare image manifest, so registries expose
	// the kraftcloud/x86_64 platform descriptor to the runtime pull path.
	index := computeImageIndex(img)
	err = remote.WriteIndex(ref, index,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(registryKeychain),
		remote.WithProgress(updates),
	)
	<-done
	task.Done(err)
	if err != nil {
		if rerr := registryError(ref, true, err); rerr != err {
			return "", withErrorDetails(opts.Verbose, rerr)
		}
		return "", fmt.Errorf("pushing image: %w", err)
	}
	digest, err := index.Digest()
	if err != nil {
		return "", fmt.Errorf("computing pushed digest: %w", err)
	}
	digestRef := ref.Context().Digest(digest.String()).String()
	fmt.Fprintf(os.Stderr, "Pushed %s (%s)\n", opts.Ref, digestRef)
	return digestRef, nil
}

func parseOutput(value string) outputSpec {
	if value == "" {
		return outputSpec{kind: outputDebug}
	}
	if strings.HasSuffix(value, ".tar") || strings.HasSuffix(value, ".tar.gz") || strings.HasSuffix(value, ".tgz") {
		return outputSpec{kind: outputArchive, value: value}
	}
	if strings.HasPrefix(value, "./") || strings.HasPrefix(value, "../") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "~/") {
		return outputSpec{kind: outputLayout, value: value}
	}
	return outputSpec{kind: outputRegistry, value: value}
}

func validateOutputOptions(opts *Options, spec outputSpec) error {
	if spec.kind == outputLayout {
		if err := checkLayoutDestination(spec.value); err != nil {
			return err
		}
	}
	if !opts.Push {
		return nil
	}
	if spec.kind != outputRegistry {
		return fmt.Errorf("--push requires a registry output: use --output ghcr.io/acme/api:tag")
	}
	return nil
}

func confirmRegistryPush(ref string) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, fmt.Errorf("output %q looks like a registry reference; rerun with --push to push without confirmation", ref)
	}
	fmt.Fprintf(os.Stderr, "Output %q looks like a registry reference. Push it? [y/N] ", ref)
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, err
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func expandPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func exportArchive(path string, img v1.Image) error {
	path = expandPath(path)
	if err := withProgress("Writing archive", func() error {
		layoutDir, err := os.MkdirTemp("", "datumctl-oci-layout-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(layoutDir)
		if err := writeOCILayout(layoutDir, img); err != nil {
			return err
		}
		return tarDirectory(layoutDir, path)
	}); err != nil {
		return fmt.Errorf("exporting OCI archive: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Wrote %s\n", path)
	return nil
}

// layoutEntries are the only files a saved image folder contains.
var layoutEntries = []string{"blobs", "index.json", "oci-layout"}

// checkLayoutDestination runs before building, so a folder the image can't
// be saved in is reported before the build's time is spent. It allows a
// missing or empty folder, or one holding only a previously saved image.
func checkLayoutDestination(arg string) error {
	entries, err := os.ReadDir(expandPath(arg))
	if os.IsNotExist(err) || (err == nil && len(entries) == 0) {
		return nil
	}
	if err != nil {
		if info, statErr := os.Stat(expandPath(arg)); statErr == nil && !info.IsDir() {
			return &userError{message: paragraphs(
				fmt.Sprintf("%s is a file, so the image can't be saved there as a folder.", arg),
				"Use a new or empty folder, or save to a file with --output "+strings.TrimSuffix(arg, filepath.Ext(arg))+".tar.",
			)}
		}
		return fmt.Errorf("checking output folder: %w", err)
	}
	hasMarker := false
	for _, e := range entries {
		if !slices.Contains(layoutEntries, e.Name()) {
			return folderHasFilesError(arg)
		}
		hasMarker = hasMarker || e.Name() == "oci-layout"
	}
	// Without the marker it may be the user's own blobs/ or index.json.
	if !hasMarker {
		return folderHasFilesError(arg)
	}
	return nil
}

func folderHasFilesError(arg string) error {
	return &userError{message: paragraphs(
		fmt.Sprintf("%s already has files in it, so the image can't be saved there.", arg),
		"Use a new or empty folder, or save to a file with --output "+strings.TrimSuffix(arg, "/")+".tar.",
	)}
}

func exportLayout(path string, img v1.Image) error {
	if err := checkLayoutDestination(path); err != nil {
		return err
	}
	path = expandPath(path)
	// Anything left here is a previously saved image; replace it.
	for _, name := range layoutEntries {
		if err := os.RemoveAll(filepath.Join(path, name)); err != nil {
			return fmt.Errorf("replacing the previous image in %s: %w", path, err)
		}
	}
	if err := writeOCILayout(path, img); err != nil {
		return fmt.Errorf("writing OCI layout directory: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Wrote %s\n", path)
	return nil
}

func tarDirectory(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	var w io.WriteCloser = out
	if strings.HasSuffix(dest, ".gz") || strings.HasSuffix(dest, ".tgz") {
		w = gzip.NewWriter(out)
	}
	tw := tar.NewWriter(w)
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == src {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, in)
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	closeTarErr := tw.Close()
	closeWriterErr := w.Close()
	closeOutErr := closeExportFile(out)
	if err != nil {
		return err
	}
	if closeTarErr != nil {
		return closeTarErr
	}
	if closeWriterErr != nil {
		return closeWriterErr
	}
	return closeOutErr
}

// untar extracts a tar archive, gzipped or not, into dest.
func untar(archivePath, dest string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	br := bufio.NewReader(f)
	var r io.Reader = br
	if magic, _ := br.Peek(2); len(magic) == 2 && magic[0] == 0x1f && magic[1] == 0x8b {
		gz, err := gzip.NewReader(br)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}

	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		// Tar names are always POSIX-style; filepath.Clean/IsAbs are
		// OS-dependent and on Windows would fail to reject a POSIX-absolute
		// path like "/etc/passwd". Reuse the guard erofs packaging uses.
		name, err := safeRootfsTarPath(hdr.Name)
		if err != nil {
			return fmt.Errorf("unsafe path in OCI archive: %s", hdr.Name)
		}
		path := filepath.Join(dest, filepath.FromSlash(name))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		default:
			return fmt.Errorf("unsupported entry in OCI archive: %s", hdr.Name)
		}
	}
}
