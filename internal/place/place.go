// Package place writes resolved packages to disk under a root (typically
// "assets/static/vendor") and computes per-file hashes for the lockfile.
//
// Named "place" because "vendor" is reserved-ish in Go (the module-root
// vendor/ directory) and overloading it would be a nuisance.
package place

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"

	"go.flaticols.dev/tspkg/internal/manifest"
	"go.flaticols.dev/tspkg/internal/resolve"
)

// WriteAll writes every resolved package under root, applying include filters
// from the manifest. Returns a map of name → file→hash for lockfile use.
//
// Existing contents under root are removed first so the on-disk state is a
// pure projection of the resolved set.
func WriteAll(root string, m *manifest.Manifest, resolved map[string]*resolve.Resolved) (map[string]map[string]string, error) {
	if err := os.RemoveAll(root); err != nil {
		return nil, fmt.Errorf("vendor: clean %s: %w", root, err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("vendor: mkdir %s: %w", root, err)
	}

	out := make(map[string]map[string]string, len(resolved))
	for _, name := range sortedKeys(resolved) {
		r := resolved[name]
		hashes, err := writeOne(root, m.Include[name], r)
		if err != nil {
			return nil, err
		}
		out[name] = hashes
	}
	return out, nil
}

func writeOne(root string, include []string, r *resolve.Resolved) (map[string]string, error) {
	pickedFiles := selectFiles(include, r)

	pkgDir := filepath.Join(root, filepath.FromSlash(r.Name))
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		return nil, fmt.Errorf("vendor: mkdir %s: %w", pkgDir, err)
	}

	hashes := make(map[string]string, len(pickedFiles))
	for _, rel := range sortStrings(pickedFiles) {
		content := r.Files[rel]
		dest := filepath.Join(pkgDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return nil, fmt.Errorf("vendor: mkdir %s: %w", filepath.Dir(dest), err)
		}
		if err := os.WriteFile(dest, content, 0o644); err != nil {
			return nil, fmt.Errorf("vendor: write %s: %w", dest, err)
		}
		hashes[rel] = resolve.HashFile(content)
	}
	return hashes, nil
}

// selectFiles applies the include filter (or the default heuristic) to a
// package's tarball contents and returns the relative paths to vendor.
func selectFiles(include []string, r *resolve.Resolved) []string {
	if len(include) > 0 {
		picked := map[string]struct{}{}
		for path := range r.Files {
			for _, pat := range include {
				ok, err := doublestar.Match(pat, path)
				if err == nil && ok {
					picked[path] = struct{}{}
					break
				}
			}
		}
		// Always also include the resolved entry — if include was overly narrow,
		// we still want the package to load.
		if _, ok := r.Files[r.Entry]; ok {
			picked[r.Entry] = struct{}{}
		}
		return mapKeys(picked)
	}

	// Default heuristic: vendor everything except obvious noise. A bundled
	// ESM package's entry often imports from sibling subdirs (sigma's
	// dist/sigma.esm.js imports from ../types/dist/sigma-types.esm.js), so a
	// narrow "entry's top dir only" filter would break the package. If a
	// package is large (like phosphor's 9000 SVGs), set tspkg.include
	// explicitly to narrow.
	picked := map[string]struct{}{}
	for path := range r.Files {
		if isNoise(path) {
			continue
		}
		picked[path] = struct{}{}
	}
	return mapKeys(picked)
}

// isNoise filters out files that bloat the vendor dir without runtime value.
func isNoise(path string) bool {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".md") && !strings.Contains(strings.ToUpper(filepath.Base(path)), "LICENSE"):
		return true
	case strings.HasSuffix(lower, ".markdown"):
		return true
	case strings.HasSuffix(lower, ".ts") && !strings.HasSuffix(lower, ".d.ts"):
		return true // typescript source (we ship the compiled output)
	case strings.HasSuffix(lower, ".map"):
		return true // sourcemaps
	case strings.HasPrefix(lower, "test/") || strings.HasPrefix(lower, "tests/"):
		return true
	case strings.HasPrefix(lower, "__tests__/"):
		return true
	case strings.HasPrefix(lower, ".github/"):
		return true
	case strings.HasPrefix(lower, "src/"):
		return true // typescript source dir
	case strings.HasPrefix(lower, "docs/") || strings.HasPrefix(lower, "doc/"):
		return true
	case strings.HasPrefix(lower, "example/") || strings.HasPrefix(lower, "examples/"):
		return true
	}
	return false
}

func mapKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sortStrings(s []string) []string {
	sorted := append([]string(nil), s...)
	sort.Strings(sorted)
	return sorted
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Verify recomputes the hashes in `expected` against the on-disk root and
// returns the list of mismatched paths (empty = OK).
func Verify(root string, expected map[string]map[string]string) ([]string, error) {
	var bad []string
	for _, name := range sortedKeys(expected) {
		for _, rel := range sortStrings(mapKeysStr(expected[name])) {
			wantHash := expected[name][rel]
			path := filepath.Join(root, filepath.FromSlash(name), filepath.FromSlash(rel))
			data, err := os.ReadFile(path)
			if err != nil {
				bad = append(bad, fmt.Sprintf("%s/%s: %v", name, rel, err))
				continue
			}
			gotHash := resolve.HashFile(data)
			if gotHash != wantHash {
				bad = append(bad, fmt.Sprintf("%s/%s: hash mismatch (want %s, got %s)", name, rel, wantHash, gotHash))
			}
		}
	}
	return bad, nil
}

func mapKeysStr(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
