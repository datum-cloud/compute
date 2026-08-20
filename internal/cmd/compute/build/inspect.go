package build

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"github.com/spf13/cobra"
)

const (
	annotationKernelPath = "org.unikraft.kernel.image"
	annotationInitrdPath = "org.unikraft.kernel.initrd"

	// annotationInitrdValue is the path KraftKit expects the initrd content
	// at, inside the tar packaged by writeInitrdLayer.
	annotationInitrdValue = "/unikraft/bin/initrd"

	// unknownPlatformLabel is the fallback platform string used when neither
	// the image config nor a resolved manifest platform is available.
	unknownPlatformLabel = "unknown"
)

type imageInspection struct {
	Ref              string
	IndexDigest      string
	ManifestDigest   string
	ManifestCount    int
	SelectedPlatform string
	ImageMediaType   types.MediaType
	ConfigMediaType  types.MediaType
	LayerCount       int
	RootFS           rootFSInspection
	HasKernelLayer   bool
	Config           imageConfigInspection
	Issues           []string
	Extended         bool
}

type rootFSInspection struct {
	Found     bool
	Path      string
	Digest    string
	MediaType types.MediaType
	Size      int64
	RawLayer  bool
}

type imageConfigInspection struct {
	Entrypoint []string
	Cmd        []string
	WorkingDir string
	Env        []string
}

type inspectOptions struct {
	extended bool
}

func inspectCommand() *cobra.Command {
	opts := inspectOptions{}
	cmd := &cobra.Command{
		Use:   "inspect IMAGE",
		Short: "Inspect a built Compute image",
		Long: `Inspect a built Compute image and summarize whether it has the pieces
needed to launch on Datum Compute.

This is a diagnostic command for images that were already written or pushed. It
shows the selected image variant, packaged filesystem metadata, startup
configuration, environment, and any issues that make the image look incomplete.

Inspection is lightweight and never downloads the packaged filesystem layer. Use
--extended to show additional OCI metadata from the image index and manifest.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInspect(cmd, args[0], opts)
		},
	}
	cmd.Flags().BoolVar(&opts.extended, "extended", false, "Show additional image index, manifest, and layer metadata")
	return cmd
}

func runInspect(cmd *cobra.Command, imageRef string, opts inspectOptions) error {
	ref, err := name.ParseReference(imageRef)
	if err != nil {
		return fmt.Errorf("parsing image reference: %w", err)
	}

	ropts := []remote.Option{
		remote.WithContext(cmd.Context()),
		remote.WithAuthFromKeychain(authn.DefaultKeychain),
	}
	desc, err := remote.Get(ref, ropts...)
	if err != nil {
		return fmt.Errorf("fetching %s: %w", imageRef, err)
	}

	inspection := imageInspection{Ref: imageRef, IndexDigest: desc.Digest.String(), Extended: opts.extended}
	switch desc.MediaType {
	case types.OCIImageIndex, types.DockerManifestList:
		idx, err := desc.ImageIndex()
		if err != nil {
			return fmt.Errorf("reading image index: %w", err)
		}
		if err := inspectIndex(&inspection, idx); err != nil {
			return err
		}
	default:
		img, err := desc.Image()
		if err != nil {
			return fmt.Errorf("reading image: %w", err)
		}
		inspection.SelectedPlatform = "single manifest"
		inspection.ManifestDigest = desc.Digest.String()
		if err := inspectImage(&inspection, img, nil); err != nil {
			return err
		}
	}

	printInspection(inspection)
	return nil
}

func inspectIndex(out *imageInspection, idx v1.ImageIndex) error {
	idxDigest, err := idx.Digest()
	if err == nil {
		out.IndexDigest = idxDigest.String()
	}
	manifest, err := idx.IndexManifest()
	if err != nil {
		return fmt.Errorf("reading image index: %w", err)
	}
	out.ManifestCount = len(manifest.Manifests)
	if len(manifest.Manifests) == 0 {
		out.Issues = append(out.Issues, "image index has no manifests")
		return nil
	}

	selected := manifest.Manifests[0]
	for _, candidate := range manifest.Manifests {
		if isComputePlatform(fmtPlatform(candidate.Platform)) {
			selected = candidate
			break
		}
	}
	out.SelectedPlatform = fmtPlatform(selected.Platform)
	out.ManifestDigest = selected.Digest.String()
	if !isComputePlatform(out.SelectedPlatform) {
		out.Issues = append(out.Issues, "no kraftcloud/x86_64 image variant found")
	}
	img, err := idx.Image(selected.Digest)
	if err != nil {
		return fmt.Errorf("reading selected manifest: %w", err)
	}
	return inspectImage(out, img, selected.Platform)
}

func inspectImage(out *imageInspection, img v1.Image, platform *v1.Platform) error {
	manifest, err := img.Manifest()
	if err != nil {
		return fmt.Errorf("reading image manifest: %w", err)
	}
	config, err := img.ConfigFile()
	if err != nil {
		return fmt.Errorf("reading image config: %w", err)
	}

	out.ImageMediaType = manifest.MediaType
	out.ConfigMediaType = manifest.Config.MediaType
	out.LayerCount = len(manifest.Layers)
	if out.SelectedPlatform == "" || out.SelectedPlatform == unknownPlatformLabel {
		out.SelectedPlatform = platformFromConfig(platform, config)
	}
	if !isComputePlatform(out.SelectedPlatform) && out.SelectedPlatform != "single manifest" {
		out.Issues = append(out.Issues, fmt.Sprintf("selected platform is %s, expected kraftcloud/x86_64", out.SelectedPlatform))
	}

	out.HasKernelLayer = hasLayerAnnotation(manifest.Layers, annotationKernelPath)
	rootfsDesc, rootfsPath := findLayerByAnnotation(manifest.Layers, annotationInitrdPath)
	if rootfsDesc == nil {
		out.Issues = append(out.Issues, "packaged filesystem layer was not found")
	} else {
		out.RootFS = rootFSInspection{
			Found:     true,
			Path:      rootfsPath,
			Digest:    rootfsDesc.Digest.String(),
			MediaType: rootfsDesc.MediaType,
			Size:      rootfsDesc.Size,
			RawLayer:  rootfsDesc.MediaType == types.OCIUncompressedLayer,
		}
		if rootfsPath != annotationInitrdValue {
			out.Issues = append(out.Issues, fmt.Sprintf("packaged filesystem annotation points to %s", rootfsPath))
		}
		if !out.RootFS.RawLayer {
			out.Issues = append(out.Issues, fmt.Sprintf("packaged filesystem layer is %s; expected raw OCI tar", rootfsDesc.MediaType))
		}
	}

	out.Config = imageConfigInspection{
		Entrypoint: config.Config.Entrypoint,
		Cmd:        config.Config.Cmd,
		WorkingDir: config.Config.WorkingDir,
		Env:        config.Config.Env,
	}
	if len(config.Config.Entrypoint) == 0 && len(config.Config.Cmd) == 0 {
		out.Issues = append(out.Issues, "startup command is empty")
	}
	return nil
}

func printInspection(in imageInspection) {
	fmt.Fprintf(os.Stdout, "Image:    %s\n", in.Ref)
	if in.IndexDigest != "" {
		fmt.Fprintf(os.Stdout, "Digest:   %s\n", in.IndexDigest)
	}
	status := "Compatible"
	if len(in.Issues) > 0 {
		status = "Incompatible"
	}
	fmt.Fprintf(os.Stdout, "Status:   %s\n\n", status)

	fmt.Fprintln(os.Stdout, "Package:")
	fmt.Fprintf(os.Stdout, "  Platform:    %s\n", valueOrNone(in.SelectedPlatform))
	if in.Extended && in.ManifestCount > 0 {
		fmt.Fprintf(os.Stdout, "  Variants:    %d, selected %s\n", in.ManifestCount, valueOrNone(in.SelectedPlatform))
		if in.ManifestDigest != "" {
			fmt.Fprintf(os.Stdout, "  Manifest:    %s\n", in.ManifestDigest)
		}
	}
	if in.RootFS.Found {
		fmt.Fprintf(os.Stdout, "  Filesystem:  packaged, %s\n", formatBytes(in.RootFS.Size))
		if in.Extended || !in.RootFS.RawLayer || len(in.Issues) > 0 {
			fmt.Fprintf(os.Stdout, "  Layer:       %s\n", in.RootFS.Digest)
			fmt.Fprintf(os.Stdout, "  Layer type:  %s\n", in.RootFS.MediaType)
		}
	} else {
		fmt.Fprintln(os.Stdout, "  Filesystem:  not found")
	}
	if in.HasKernelLayer {
		fmt.Fprintln(os.Stdout, "  Kernel:      bundled")
	} else {
		fmt.Fprintln(os.Stdout, "  Kernel:      runtime")
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Startup:")
	appArgs := unwrapWorkdirCommand(in.Config.Cmd)
	if len(in.Config.Entrypoint) > 0 {
		fmt.Fprintf(os.Stdout, "  Entrypoint:  %s\n", fmtArgs(in.Config.Entrypoint))
	}
	if len(in.Config.Cmd) > 0 {
		fmt.Fprintf(os.Stdout, "  Command:     %s\n", fmtArgs(in.Config.Cmd))
		if len(appArgs) > 0 && !slices.Equal(appArgs, in.Config.Cmd) {
			fmt.Fprintf(os.Stdout, "  App command: %s\n", fmtArgs(appArgs))
		}
	}
	if len(in.Config.Entrypoint) == 0 && len(in.Config.Cmd) == 0 {
		fmt.Fprintln(os.Stdout, "  Command:     none")
	}
	fmt.Fprintf(os.Stdout, "  Workdir:     %s\n", valueOrDefault(in.Config.WorkingDir, "/"))

	if len(in.Config.Env) > 0 {
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "Environment:")
		for _, entry := range in.Config.Env {
			fmt.Fprintf(os.Stdout, "  %s\n", entry)
		}
	}

	if in.Extended {
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "Image Metadata:")
		fmt.Fprintf(os.Stdout, "  Image media:   %s\n", in.ImageMediaType)
		fmt.Fprintf(os.Stdout, "  Config media:  %s\n", in.ConfigMediaType)
		fmt.Fprintf(os.Stdout, "  Layers:        %d\n", in.LayerCount)
		if in.IndexDigest != "" {
			fmt.Fprintf(os.Stdout, "  Index:         %s\n", in.IndexDigest)
		}
	}

	if len(in.Issues) > 0 {
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "Issues:")
		for _, issue := range in.Issues {
			fmt.Fprintf(os.Stdout, "  - %s\n", issue)
		}
	}
	fmt.Fprintln(os.Stdout)
}

func findLayerByAnnotation(layers []v1.Descriptor, key string) (*v1.Descriptor, string) {
	for i, l := range layers {
		if v, ok := l.Annotations[key]; ok {
			return &layers[i], v
		}
	}
	return nil, ""
}

func hasLayerAnnotation(layers []v1.Descriptor, key string) bool {
	desc, _ := findLayerByAnnotation(layers, key)
	return desc != nil
}

func platformFromConfig(platform *v1.Platform, config *v1.ConfigFile) string {
	if platform != nil {
		return fmtPlatform(platform)
	}
	if config.OS == "" && config.Architecture == "" {
		return unknownPlatformLabel
	}
	return config.OS + "/" + config.Architecture
}

func fmtPlatform(p *v1.Platform) string {
	if p == nil {
		return unknownPlatformLabel
	}
	return p.OS + "/" + p.Architecture
}

func isComputePlatform(s string) bool {
	return strings.HasPrefix(s, "kraftcloud/") || strings.HasPrefix(s, "fc/")
}

func fmtArgs(args []string) string {
	b, _ := json.Marshal(args)
	return string(b)
}

func valueOrNone(value string) string {
	return valueOrDefault(value, "none")
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func unwrapWorkdirCommand(args []string) []string {
	if rest, ok := unwrapWorkdirWrapperArgs(args); ok {
		return rest
	}
	return args
}
