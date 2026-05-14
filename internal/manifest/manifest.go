// Package manifest reads a frontend dep manifest, which lives in the
// project's package.json under the standard `dependencies` field plus a
// namespaced `tspkg` block for per-package config and path overrides.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
)

// Defaults for path/config fields when the manifest leaves them blank.
const (
	DefaultVendor        = "assets/static/vendor"
	DefaultLock          = "tspkg.lock"
	DefaultImportmap     = "importmap/importmap.go"
	DefaultVendorURLBase = "/static/vendor"
	DefaultPackageName   = "importmap"
)

// Manifest is the subset of package.json that tspkg cares about.
type Manifest struct {
	// Deps maps package name → semver range (e.g. "^1.6.0").
	Deps map[string]string

	// Include maps package name → list of glob patterns (rooted at the package
	// directory) selecting files to vendor. When unset for a package, vendor
	// uses its default heuristic.
	Include map[string][]string

	// Skip is a list of package names to ignore when walking transitive deps.
	// Use this for phantom CJS dependencies that a bundled ESM dist file
	// doesn't actually import at runtime (the npm package declares it but the
	// shipped artifact has it inlined or doesn't use it). The user takes
	// responsibility for runtime correctness.
	Skip []string

	// Bundled is a list of package names whose dist is self-contained — when
	// resolving them, tspkg vendors the package itself but doesn't walk its
	// declared dependencies. Common for libraries that ship a bundled ESM
	// build (sigma, graphology, etc.) which inline their deps at build time.
	Bundled []string

	// Groups buckets package names into named groups for the generated import
	// map. Each group becomes its own JSON constant in the generated Go file
	// (e.g. "graph" → const Graph), letting consumers inline only the maps
	// they need on a given page. Packages not listed in any group land in the
	// implicit default group (const Base).
	Groups map[string][]string

	// Paths and Importmap configure where tspkg reads and writes. Blank fields
	// fall back to the Default* constants above.
	Paths     Paths
	Importmap ImportmapConfig
}

// Paths configures filesystem paths and URL bases for the sync output.
type Paths struct {
	// Vendor is the directory under which vendored packages are written.
	Vendor string
	// Lock is the lockfile path.
	Lock string
	// Importmap is the path of the generated Go source file.
	Importmap string
	// VendorURLBase is the URL prefix under which the vendored tree is served.
	// Generated import map paths start with this prefix; importmap.Rebase
	// rewrites the canonical "/static/" root onto a per-consumer asset base
	// at render time.
	VendorURLBase string
}

// ImportmapConfig configures the generated importmap Go file.
type ImportmapConfig struct {
	// Package is the Go package name declared in the generated file.
	Package string
}

type rawPackageJSON struct {
	Dependencies map[string]string `json:"dependencies"`
	Tspkg        struct {
		Include   map[string][]string `json:"include"`
		Skip      []string            `json:"skip"`
		Bundled   []string            `json:"bundled"`
		Groups    map[string][]string `json:"groups"`
		Paths     rawPaths            `json:"paths"`
		Importmap rawImportmap        `json:"importmap"`
	} `json:"tspkg"`
}

type rawPaths struct {
	Vendor        string `json:"vendor"`
	Lock          string `json:"lock"`
	Importmap     string `json:"importmap"`
	VendorURLBase string `json:"vendorUrlBase"`
}

type rawImportmap struct {
	Package string `json:"package"`
}

// Read parses the manifest at path (typically "package.json").
func Read(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("manifest: read %s: %w", path, err)
	}

	var raw rawPackageJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("manifest: parse %s: %w", path, err)
	}

	if len(raw.Dependencies) == 0 {
		return nil, fmt.Errorf("manifest: %s has no dependencies", path)
	}

	return &Manifest{
		Deps:    raw.Dependencies,
		Include: raw.Tspkg.Include,
		Skip:    raw.Tspkg.Skip,
		Bundled: raw.Tspkg.Bundled,
		Groups:  raw.Tspkg.Groups,
		Paths: Paths{
			Vendor:        firstNonEmpty(raw.Tspkg.Paths.Vendor, DefaultVendor),
			Lock:          firstNonEmpty(raw.Tspkg.Paths.Lock, DefaultLock),
			Importmap:     firstNonEmpty(raw.Tspkg.Paths.Importmap, DefaultImportmap),
			VendorURLBase: firstNonEmpty(raw.Tspkg.Paths.VendorURLBase, DefaultVendorURLBase),
		},
		Importmap: ImportmapConfig{
			Package: firstNonEmpty(raw.Tspkg.Importmap.Package, DefaultPackageName),
		},
	}, nil
}

func firstNonEmpty(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
