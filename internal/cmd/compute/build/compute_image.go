package build

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/layout"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// computePlatformOS and computePlatformArch are the synthetic platform
// values Datum Compute uses to select images, both in the image config and
// the index manifest's platform.
const (
	computePlatformOS   = "kraftcloud"
	computePlatformArch = "x86_64"
)

// assembleComputeImage packs the rootfs into a Compute OCI image. The runtime kernel
// is intentionally omitted; Datum Compute injects the correct runtime at boot.
func assembleComputeImage(tmpDir string, input packagingArtifact) (v1.Image, error) {
	var img v1.Image
	if err := withProgress("Assembling Compute image", func() error {
		layerPath := filepath.Join(tmpDir, "initrd-layer.tar")
		if err := writeInitrdLayer(layerPath, input.Path); err != nil {
			return err
		}
		layerData, err := os.ReadFile(layerPath)
		if err != nil {
			return err
		}
		// Compute discovers boot assets from raw OCI tar layers; gzip-compressed
		// layers are valid OCI but are ignored by the runtime content detector.
		layer := static.NewLayer(layerData, types.OCIUncompressedLayer)
		base := mutate.MediaType(empty.Image, types.OCIManifestSchema1)
		base = mutate.ConfigMediaType(base, types.OCIConfigJSON)
		base, err = mutate.Append(base, mutate.Addendum{
			Layer: layer,
			Annotations: map[string]string{
				// Match KraftKit's well-known initrd layer annotation.
				annotationInitrdPath: annotationInitrdValue,
			},
		})
		if err != nil {
			return err
		}
		cfg, err := base.ConfigFile()
		if err != nil {
			return err
		}
		cfg.Architecture = computePlatformArch
		cfg.OS = computePlatformOS
		cfg.Config.Cmd = slices.Clone(input.Config.Args)
		cfg.Config.Env = slices.Clone(input.Config.Env)
		cfg.Config.WorkingDir = input.Config.WorkingDir
		img, err = mutate.ConfigFile(base, cfg)
		return err
	}); err != nil {
		return nil, fmt.Errorf("assembling Compute image: %w", err)
	}
	return img, nil
}

func writeInitrdLayer(layerPath, initrdPath string) error {
	info, err := os.Stat(initrdPath)
	if err != nil {
		return err
	}
	out, err := os.OpenFile(layerPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(out)
	for _, dir := range []string{"unikraft", "unikraft/bin"} {
		if err := tw.WriteHeader(&tar.Header{Name: dir, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			_ = tw.Close()
			_ = out.Close()
			return err
		}
	}
	if err := tw.WriteHeader(&tar.Header{Name: "unikraft/bin/initrd", Typeflag: tar.TypeReg, Mode: 0o644, Size: info.Size()}); err != nil {
		_ = tw.Close()
		_ = out.Close()
		return err
	}
	in, err := os.Open(initrdPath)
	if err != nil {
		_ = tw.Close()
		_ = out.Close()
		return err
	}
	_, copyErr := io.Copy(tw, in)
	closeInErr := in.Close()
	closeTarErr := tw.Close()
	closeOutErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeInErr != nil {
		return closeInErr
	}
	if closeTarErr != nil {
		return closeTarErr
	}
	return closeOutErr
}

func writeOCILayout(path string, img v1.Image) error {
	_, err := layout.Write(path, computeImageIndex(img))
	return err
}

func computeImageIndex(img v1.Image) v1.ImageIndex {
	return mutate.AppendManifests(empty.Index, mutate.IndexAddendum{
		Add: img,
		// Datum Compute selects images by this synthetic platform.
		Descriptor: v1.Descriptor{Platform: &v1.Platform{
			Architecture: computePlatformArch,
			OS:           computePlatformOS,
		}},
	})
}
