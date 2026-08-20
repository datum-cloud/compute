package build

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/docker/cli/cli/config"
	dockerclient "github.com/docker/docker/client"
	dockerbuildkit "github.com/docker/docker/client/buildkit"
	erofs "github.com/erofs/go-erofs"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/types"
	bkclient "github.com/moby/buildkit/client"
	"github.com/moby/buildkit/session"
	"github.com/moby/buildkit/session/auth/authprovider"
	bkappdefaults "github.com/moby/buildkit/util/appdefaults"
	"github.com/moby/buildkit/util/progress/progressui"
	"github.com/tonistiigi/fsutil"
	"golang.org/x/sync/errgroup"
)

const (
	erofsModeFIFO    = 0o010000
	erofsModeCharDev = 0o020000
	erofsModeBlock   = 0o060000
	erofsModeSocket  = 0o140000
)

type buildRequest struct {
	Address     string
	ContextDir  string
	Dockerfile  string
	Target      string
	BuildArgs   []string
	progressOut io.Writer
}

type dockerfileFinalStageRequest struct {
	ContextDir  string
	Dockerfile  string
	Target      string
	BuildArgs   []string
	RootFSTar   string
	OCITar      string
	progressOut io.Writer
}

func buildDockerfileFinalStage(ctx context.Context, req dockerfileFinalStageRequest) (packagingArtifact, error) {
	if err := buildDockerfileExports(ctx, buildRequest{
		ContextDir:  req.ContextDir,
		Dockerfile:  req.Dockerfile,
		Target:      req.Target,
		BuildArgs:   req.BuildArgs,
		progressOut: req.progressOut,
	}, req.RootFSTar, req.OCITar); err != nil {
		return packagingArtifact{}, err
	}

	workDir, err := os.MkdirTemp("", "datumctl-oci-config-*")
	if err != nil {
		return packagingArtifact{}, err
	}
	defer os.RemoveAll(workDir)
	if err := untar(req.OCITar, workDir); err != nil {
		return packagingArtifact{}, fmt.Errorf("extracting built image OCI layout: %w", err)
	}
	img, err := firstImageFromOCILayout(workDir)
	if err != nil {
		return packagingArtifact{}, fmt.Errorf("reading built image config: %w", err)
	}
	cfg, err := img.ConfigFile()
	if err != nil {
		return packagingArtifact{}, fmt.Errorf("reading built image config: %w", err)
	}
	// A shell-form CMD (e.g. "CMD npm start") compiles to ["/bin/sh", "-c",
	// "npm start"]; that structure is left intact for resolveStartupArgs to
	// trace properly rather than split apart here.
	config := imageConfig{
		Args:       append(append([]string{}, cfg.Config.Entrypoint...), cfg.Config.Cmd...),
		Env:        append([]string{}, cfg.Config.Env...),
		WorkingDir: cfg.Config.WorkingDir,
	}
	return packagingArtifact{Path: req.RootFSTar, Config: config}, nil
}

func createErofsFromTar(tarPath, output string) error {
	out, err := os.OpenFile(output, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("creating EROFS image: %w", err)
	}
	defer out.Close()

	in, err := os.Open(tarPath)
	if err != nil {
		return fmt.Errorf("opening rootfs tar: %w", err)
	}
	defer in.Close()

	tempDir, err := os.MkdirTemp("", "datumctl-erofs-*")
	if err != nil {
		return fmt.Errorf("creating EROFS temp directory: %w", err)
	}
	defer os.RemoveAll(tempDir)

	w := erofs.Create(out, erofs.WithTempDir(tempDir))
	regularFiles := make(map[string]string)
	tr := tar.NewReader(in)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return w.Close()
		}
		if err != nil {
			return fmt.Errorf("reading rootfs tar: %w", err)
		}
		if err := addTarEntryToErofs(w, tempDir, regularFiles, tr, hdr); err != nil {
			return err
		}
	}
}

func addTarEntryToErofs(w *erofs.Writer, tempDir string, regularFiles map[string]string, tr *tar.Reader, hdr *tar.Header) error {
	name, err := safeRootfsTarPath(hdr.Name)
	if err != nil {
		return err
	}
	if name == "." {
		return nil
	}
	erofsPath := "/" + name

	switch hdr.Typeflag {
	case tar.TypeDir:
		if err := w.Mkdir(erofsPath, os.FileMode(hdr.Mode)); err != nil {
			return err
		}
	case tar.TypeReg:
		if err := writeRegularFileToErofs(w, tempDir, regularFiles, name, erofsPath, tr); err != nil {
			return err
		}
	case tar.TypeSymlink:
		if err := w.Symlink(hdr.Linkname, erofsPath); err != nil {
			return fmt.Errorf("creating rootfs symlink %s: %w", hdr.Name, err)
		}
	case tar.TypeLink:
		if err := writeHardlinkToErofs(w, regularFiles, erofsPath, hdr); err != nil {
			return err
		}
	case tar.TypeChar:
		return w.Mknod(erofsPath, erofsModeCharDev|uint16(hdr.Mode&0o7777), encodeDeviceID(hdr.Devmajor, hdr.Devminor))
	case tar.TypeBlock:
		return w.Mknod(erofsPath, erofsModeBlock|uint16(hdr.Mode&0o7777), encodeDeviceID(hdr.Devmajor, hdr.Devminor))
	case tar.TypeFifo:
		return w.Mknod(erofsPath, erofsModeFIFO|uint16(hdr.Mode&0o7777), 0)
	default:
		return nil
	}

	if err := w.Chmod(erofsPath, os.FileMode(hdr.Mode)); err != nil {
		return err
	}
	if err := w.Chown(erofsPath, 0, 0); err != nil {
		return err
	}
	if !hdr.ModTime.IsZero() {
		if err := w.Chtimes(erofsPath, hdr.AccessTime, hdr.ModTime); err != nil {
			return err
		}
	}
	for key, value := range hdr.PAXRecords {
		if attr, ok := strings.CutPrefix(key, "SCHILY.xattr."); ok {
			if err := w.Setxattr(erofsPath, attr, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func writeRegularFileToErofs(w *erofs.Writer, tempDir string, regularFiles map[string]string, name, erofsPath string, tr *tar.Reader) error {
	f, err := w.Create(erofsPath)
	if err != nil {
		return err
	}
	spooled, err := os.CreateTemp(tempDir, "regular-*")
	if err != nil {
		_ = f.Close()
		return err
	}
	regularFiles[name] = spooled.Name()
	_, copyErr := io.Copy(io.MultiWriter(f, spooled), tr)
	spoolCloseErr := spooled.Close()
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if spoolCloseErr != nil {
		return spoolCloseErr
	}
	return closeErr
}

func writeHardlinkToErofs(w *erofs.Writer, regularFiles map[string]string, erofsPath string, hdr *tar.Header) error {
	linkName, err := safeRootfsTarPath(hdr.Linkname)
	if err != nil {
		return fmt.Errorf("unsafe hardlink target %s: %w", hdr.Linkname, err)
	}
	spooledPath, ok := regularFiles[linkName]
	if !ok {
		return fmt.Errorf("creating rootfs hardlink %s: target %s was not found earlier in tar", hdr.Name, hdr.Linkname)
	}
	in, err := os.Open(spooledPath)
	if err != nil {
		return err
	}
	f, err := w.Create(erofsPath)
	if err != nil {
		_ = in.Close()
		return err
	}
	_, copyErr := io.Copy(f, in)
	inCloseErr := in.Close()
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	if inCloseErr != nil {
		return inCloseErr
	}
	return closeErr
}

func encodeDeviceID(major, minor int64) uint32 {
	return uint32((minor & 0xff) | (major << 8) | ((minor &^ 0xff) << 12))
}

func safeRootfsTarPath(name string) (string, error) {
	name = path.Clean(name)
	if name == "." {
		return name, nil
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") || name == ".." {
		return "", fmt.Errorf("unsafe path in rootfs tar: %s", name)
	}
	return name, nil
}

func firstImageFromOCILayout(path string) (v1.Image, error) {
	idx, err := layout.ImageIndexFromPath(path)
	if err != nil {
		return nil, err
	}
	return firstImageFromIndex(idx)
}

func firstImageFromIndex(idx v1.ImageIndex) (v1.Image, error) {
	manifest, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	for _, desc := range manifest.Manifests {
		switch desc.MediaType {
		case types.OCIManifestSchema1, types.DockerManifestSchema2:
			return idx.Image(desc.Digest)
		case types.OCIImageIndex, types.DockerManifestList:
			nested, err := idx.ImageIndex(desc.Digest)
			if err != nil {
				return nil, err
			}
			return firstImageFromIndex(nested)
		}
	}
	return nil, fmt.Errorf("OCI layout contains no image manifest")
}

func buildDockerfileExports(ctx context.Context, req buildRequest, rootfsTarPath string, ociTarPath string) error {
	bk, cleanup, err := connectBuildkit(ctx, req.Address)
	if err != nil {
		return err
	}
	defer bk.Close()
	if cleanup != nil {
		defer cleanup()
	}

	rootfsTar, err := os.OpenFile(rootfsTarPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("creating rootfs export archive: %w", err)
	}
	ociTar, err := os.OpenFile(ociTarPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		_ = rootfsTar.Close()
		return fmt.Errorf("creating OCI export archive: %w", err)
	}

	solveOpt, err := buildSolveOpt(req, ociTar)
	if err != nil {
		_ = rootfsTar.Close()
		_ = ociTar.Close()
		return err
	}
	solveOpt.Exports = append([]bkclient.ExportEntry{{
		Type:   bkclient.ExporterTar,
		Output: func(map[string]string) (io.WriteCloser, error) { return rootfsTar, nil },
	}}, solveOpt.Exports...)

	solveErr := solveWithProgress(ctx, bk, solveOpt, req.progressOut)
	rootfsCloseErr := closeExportFile(rootfsTar)
	ociCloseErr := closeExportFile(ociTar)
	if solveErr != nil {
		return fmt.Errorf("solving Dockerfile with BuildKit: %w", solveErr)
	}
	if rootfsCloseErr != nil {
		return fmt.Errorf("closing rootfs export archive: %w", rootfsCloseErr)
	}
	if ociCloseErr != nil {
		return fmt.Errorf("closing OCI export archive: %w", ociCloseErr)
	}
	return nil
}

func solveWithProgress(ctx context.Context, bk *bkclient.Client, solveOpt *bkclient.SolveOpt, progress io.Writer) error {
	if progress == nil {
		_, err := bk.Solve(ctx, nil, *solveOpt, nil)
		return err
	}
	ch := make(chan *bkclient.SolveStatus)
	eg, ctx := errgroup.WithContext(ctx)
	eg.Go(func() error {
		_, err := bk.Solve(ctx, nil, *solveOpt, ch)
		return err
	})
	eg.Go(func() error {
		d, err := progressui.NewDisplay(progress, progressui.AutoMode)
		if err != nil {
			return err
		}
		_, err = d.UpdateFrom(ctx, ch)
		return err
	})
	return eg.Wait()
}

func closeExportFile(f *os.File) error {
	err := f.Close()
	if errors.Is(err, os.ErrClosed) {
		return nil
	}
	return err
}

func connectBuildkit(ctx context.Context, address string) (*bkclient.Client, func(), error) {
	if address == "" {
		address = os.Getenv("BUILDKIT_HOST")
	}
	if address != "" {
		c, err := bkclient.New(ctx, address)
		if err != nil {
			return nil, nil, fmt.Errorf("creating configured BuildKit client: %w", err)
		}
		if _, err := c.Info(ctx); err != nil {
			_ = c.Close()
			return nil, nil, fmt.Errorf("connecting to configured BuildKit client: %w", err)
		}
		return c, nil, nil
	}

	if c, err := bkclient.New(ctx, bkappdefaults.Address); err == nil {
		if _, err := c.Info(ctx); err == nil {
			return c, nil, nil
		}
		_ = c.Close()
	}

	c, cleanup, err := connectDockerBuildkit(ctx)
	if err == nil && c != nil {
		return c, cleanup, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return nil, nil, fmt.Errorf("could not connect to BuildKit: set BUILDKIT_HOST, start buildkitd, or enable Docker's BuildKit backend")
}

func connectDockerBuildkit(ctx context.Context) (*bkclient.Client, func(), error) {
	docker, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, nil, nil
	}
	if _, err := docker.ServerVersion(ctx); err != nil {
		_ = docker.Close()
		return nil, nil, nil
	}
	c, err := bkclient.New(ctx, "", dockerbuildkit.ClientOpts(docker)...)
	if err != nil {
		_ = docker.Close()
		return nil, nil, fmt.Errorf("creating Docker BuildKit client: %w", err)
	}
	if _, err := c.Info(ctx); err != nil {
		_ = c.Close()
		_ = docker.Close()
		return nil, nil, nil
	}
	return c, func() { _ = docker.Close() }, nil
}

func buildSolveOpt(req buildRequest, output io.WriteCloser) (*bkclient.SolveOpt, error) {
	contextMount, err := fsutil.NewFS(req.ContextDir)
	if err != nil {
		return nil, fmt.Errorf("creating context mount: %w", err)
	}
	dockerfileMount, err := fsutil.NewFS(filepath.Dir(req.Dockerfile))
	if err != nil {
		return nil, fmt.Errorf("creating Dockerfile mount: %w", err)
	}

	attrs := map[string]string{
		"filename": filepath.Base(req.Dockerfile),
		"platform": "linux/amd64",
	}
	if req.Target != "" {
		attrs["target"] = req.Target
	}
	for _, arg := range req.BuildArgs {
		key, value, ok := strings.Cut(arg, "=")
		if key == "" {
			return nil, fmt.Errorf("invalid build arg %q", arg)
		}
		if ok {
			attrs["build-arg:"+key] = value
		} else if value, ok := os.LookupEnv(key); ok {
			attrs["build-arg:"+key] = value
		}
	}

	dockerConfig := config.LoadDefaultConfigFile(io.Discard)
	return &bkclient.SolveOpt{
		Exports: []bkclient.ExportEntry{{
			Type: "oci",
			Attrs: map[string]string{
				"tar": "true",
			},
			Output: func(map[string]string) (io.WriteCloser, error) {
				return output, nil
			},
		}},
		LocalMounts: map[string]fsutil.FS{
			"context":    contextMount,
			"dockerfile": dockerfileMount,
		},
		Frontend:      "dockerfile.v0",
		FrontendAttrs: attrs,
		Session: []session.Attachable{
			authprovider.NewDockerAuthProvider(authprovider.DockerAuthProviderConfig{
				AuthConfigProvider: authprovider.LoadAuthConfig(dockerConfig),
			}),
		},
	}, nil
}
