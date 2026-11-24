// SPDX-License-Identifier: AGPL-3.0-only

package build

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// ComputePlatform is the platform a Compute image carries in its index.
const ComputePlatform = computePlatformOS + "/" + computePlatformArch

// ImagePlatforms returns the platforms a registry image offers, as os/arch:
// one per manifest in its index, or the config platform of a single manifest.
func ImagePlatforms(ctx context.Context, image string) ([]string, error) {
	ref, err := name.ParseReference(image)
	if err != nil {
		return nil, fmt.Errorf("parsing image reference: %w", err)
	}
	desc, err := remote.Get(ref, remote.WithContext(ctx), remote.WithAuthFromKeychain(registryKeychain))
	if err != nil {
		return nil, registryError(ref, false, err)
	}

	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		idx, err := desc.ImageIndex()
		if err != nil {
			return nil, fmt.Errorf("reading image index: %w", err)
		}
		manifest, err := idx.IndexManifest()
		if err != nil {
			return nil, fmt.Errorf("reading image index: %w", err)
		}
		var platforms []string
		for _, m := range manifest.Manifests {
			if m.Platform == nil || m.Platform.OS == unknownPlatformLabel {
				continue
			}
			if p := fmtPlatform(m.Platform); !slices.Contains(platforms, p) {
				platforms = append(platforms, p)
			}
		}
		return platforms, nil
	default:
		img, err := desc.Image()
		if err != nil {
			return nil, fmt.Errorf("reading image: %w", err)
		}
		cfg, err := img.ConfigFile()
		if err != nil {
			return nil, fmt.Errorf("reading image config: %w", err)
		}
		if cfg.OS == "" && cfg.Architecture == "" {
			return nil, nil
		}
		return []string{cfg.OS + "/" + cfg.Architecture}, nil
	}
}

// IsComputePlatform reports whether an os/arch string names a Compute image
// variant, the current label or the one older images carry.
func IsComputePlatform(platform string) bool {
	os, arch, ok := strings.Cut(platform, "/")
	return ok && isComputePlatform(&v1.Platform{OS: os, Architecture: arch})
}
