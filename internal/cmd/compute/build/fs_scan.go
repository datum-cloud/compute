package build

import (
	"archive/tar"
	"io"
	"os"
	"path"
	"strings"
)

type fsScanResult struct {
	paths      map[string]struct{}
	executable map[string]struct{}
	dirs       map[string]struct{}
	symlinks   map[string]string
}

type tarFSView struct {
	Paths    map[string]struct{}
	Symlinks map[string]string
	Open     func(string) ([]byte, error)

	// Executable is the subset of Paths with an execute bit set. A file
	// missing from this set can never be exec'd by the container runtime.
	Executable map[string]struct{}

	// Dirs is every directory explicitly present in the image, even if
	// empty (e.g. a mkdir'd mount point) — see impliedDirs for directories
	// only implied by a file's path.
	Dirs map[string]struct{}
}

// HasPath reports whether path exists in the image filesystem. A nil view
// (as in a test checkContext built without one) reports every path absent.
func (v *tarFSView) HasPath(path string) bool {
	if v == nil {
		return false
	}
	_, ok := v.Paths[strings.TrimPrefix(path, "/")]
	return ok
}

// IsExecutable reports whether path exists and has an execute bit set.
func (v *tarFSView) IsExecutable(path string) bool {
	if v == nil {
		return false
	}
	_, ok := v.Executable[strings.TrimPrefix(path, "/")]
	return ok
}

func newFSScanResult() fsScanResult {
	return fsScanResult{
		paths:      make(map[string]struct{}),
		executable: make(map[string]struct{}),
		dirs:       make(map[string]struct{}),
		symlinks:   make(map[string]string),
	}
}

func (r fsScanResult) addPath(path string, mode os.FileMode) {
	r.paths[path] = struct{}{}
	if mode&0o111 != 0 {
		r.executable[path] = struct{}{}
	}
}

func (r fsScanResult) addDir(path string) {
	r.dirs[path] = struct{}{}
}

func (r fsScanResult) addSymlink(path, target string) {
	r.paths[path] = struct{}{}
	r.symlinks[path] = target
}

func (r fsScanResult) view(open func(string) ([]byte, error)) *tarFSView {
	return &tarFSView{Paths: r.paths, Symlinks: r.symlinks, Executable: r.executable, Dirs: r.dirs, Open: open}
}

func openTarFSView(tarPath string) (*tarFSView, error) {
	result := newFSScanResult()
	hardlinks := make(map[string]string)
	if err := walkTarFile(tarPath, func(hdr *tar.Header, _ io.Reader) error {
		name := normalizeTarPath(hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeReg:
			result.addPath(name, hdr.FileInfo().Mode())
		case tar.TypeDir:
			result.addDir(name)
		case tar.TypeSymlink:
			result.addSymlink(name, hdr.Linkname)
		case tar.TypeLink:
			result.addPath(name, hdr.FileInfo().Mode())
			hardlinks[name] = normalizeTarPath(hdr.Linkname)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	view := result.view(func(path string) ([]byte, error) {
		if target, ok := hardlinks[path]; ok {
			path = target
		}
		return readTarFile(tarPath, path)
	})
	addSymlinkPathAliases(view.Executable, view.Symlinks)
	return view, nil
}

func walkTarFile(tarPath string, fn func(*tar.Header, io.Reader) error) error {
	f, err := os.Open(tarPath)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if err := fn(hdr, tr); err != nil {
			return err
		}
	}
}

func readTarFile(tarPath string, target string) ([]byte, error) {
	var out []byte
	err := walkTarFile(tarPath, func(hdr *tar.Header, r io.Reader) error {
		if out != nil || hdr.Typeflag != tar.TypeReg || normalizeTarPath(hdr.Name) != target {
			return nil
		}
		var err error
		out, err = io.ReadAll(r)
		return err
	})
	return out, err
}

func addSymlinkPathAliases(paths map[string]struct{}, symlinks map[string]string) {
	if len(symlinks) == 0 {
		return
	}
	for range len(symlinks) + 1 {
		added := false
		for link, target := range symlinks {
			target = resolveSymlinkTarget(link, target)
			for path := range paths {
				alias, ok := symlinkAliasPath(link, target, path)
				if !ok {
					continue
				}
				if _, exists := paths[alias]; exists {
					continue
				}
				paths[alias] = struct{}{}
				added = true
			}
		}
		if !added {
			return
		}
	}
}

func resolveSymlinkTarget(link, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	if after, ok := strings.CutPrefix(target, "/"); ok {
		return after
	}
	return normalizeTarPath(path.Join(path.Dir("/"+link), target))
}

func symlinkAliasPath(link, target, path string) (string, bool) {
	if target == "" || path == link {
		return "", false
	}
	if path == target {
		return link, true
	}
	prefix := strings.TrimSuffix(target, "/") + "/"
	if after, ok := strings.CutPrefix(path, prefix); ok {
		return strings.TrimSuffix(link, "/") + "/" + after, true
	}
	return "", false
}

func candidateEntrypointPaths(entrypoint string) map[string]struct{} {
	return candidateEntrypointPathsWithDirs(entrypoint, nil)
}

func candidateEntrypointPathsWithDirs(entrypoint string, dirs []string) map[string]struct{} {
	entrypoint = strings.TrimPrefix(strings.TrimSpace(entrypoint), "/")
	if entrypoint == "" {
		return nil
	}
	if strings.Contains(entrypoint, "/") {
		return map[string]struct{}{entrypoint: {}}
	}
	candidates := map[string]struct{}{entrypoint: {}}
	for _, dir := range append([]string{"usr/local/sbin", "usr/local/bin", "usr/sbin", "usr/bin", "sbin", "bin"}, dirs...) {
		dir = strings.Trim(strings.TrimSpace(dir), "/")
		if dir == "" {
			continue
		}
		candidates[dir+"/"+entrypoint] = struct{}{}
	}
	return candidates
}

func resolveEntrypointPathWithDirs(entrypoint string, paths map[string]struct{}, dirs []string) string {
	for candidate := range candidateEntrypointPathsWithDirs(entrypoint, dirs) {
		if _, ok := paths[candidate]; ok {
			return "/" + candidate
		}
	}
	return ""
}

func envPathDirs(env []string) []string {
	for _, item := range env {
		value, ok := strings.CutPrefix(item, "PATH=")
		if !ok {
			continue
		}
		dirs := strings.Split(value, ":")
		out := dirs[:0]
		for _, dir := range dirs {
			if strings.TrimSpace(dir) != "" {
				out = append(out, dir)
			}
		}
		return out
	}
	return nil
}

func resolvePathThroughSymlinks(path string, symlinks map[string]string) string {
	path = normalizeTarPath(path)
	for range len(symlinks) + 1 {
		changed := false
		if target, ok := symlinks[path]; ok {
			resolved := resolveSymlinkTarget(path, target)
			if resolved == "" || resolved == path {
				return path
			}
			path = resolved
			changed = true
		}
		for link, target := range symlinks {
			prefix := strings.TrimSuffix(link, "/") + "/"
			if !strings.HasPrefix(path, prefix) {
				continue
			}
			resolved := resolveSymlinkTarget(link, target)
			if resolved == "" {
				continue
			}
			path = strings.TrimSuffix(resolved, "/") + "/" + strings.TrimPrefix(path, prefix)
			changed = true
			break
		}
		if changed {
			continue
		}
		return path
	}
	return path
}

func normalizeTarPath(raw string) string {
	s := strings.TrimPrefix(raw, "./")
	return strings.TrimPrefix(s, "/")
}

func scanTarFSView(view *tarFSView, binaryPath string) ([]byte, error) {
	wants := candidateEntrypointPaths(binaryPath)
	binaryData, err := readRootFSEntrypoint(view, wants)
	if err != nil {
		return nil, err
	}
	addSymlinkPathAliases(view.Paths, view.Symlinks)
	return binaryData, nil
}

func readRootFSEntrypoint(view *tarFSView, wants map[string]struct{}) ([]byte, error) {
	for want := range wants {
		_, isSymlink := view.Symlinks[want]
		if _, ok := view.Paths[want]; ok && !isSymlink {
			return view.Open(want)
		}
	}
	for want := range wants {
		resolved := resolvePathThroughSymlinks(want, view.Symlinks)
		if resolved == want {
			continue
		}
		if _, ok := view.Paths[resolved]; !ok {
			continue
		}
		return view.Open(resolved)
	}
	return nil, nil
}
