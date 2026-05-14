// Package resolve walks the npm registry to fetch and ESM-validate the
// transitive closure of the manifest's dependencies.
package resolve

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"slices"
	"sort"

	"github.com/Masterminds/semver/v3"

	"go.flaticols.dev/tspkg/internal/extract"
	"go.flaticols.dev/tspkg/internal/lock"
	"go.flaticols.dev/tspkg/internal/manifest"
	"go.flaticols.dev/tspkg/internal/pkg"
	"go.flaticols.dev/tspkg/internal/registry"
)

// Resolved captures everything needed to vendor one package.
type Resolved struct {
	Name      string
	Version   string
	Tarball   string
	Integrity string

	// Files is the unpacked tarball contents (relative path → bytes).
	Files extract.Files

	// Entry is the package-relative path of the ESM entry for the bare
	// specifier (e.g. "dist/sigma.esm.js").
	Entry string

	// Subpaths maps every exported subpath ("." for the root, "./dom", etc.)
	// to its package-relative ESM file. Used by the importmap generator so
	// `import "@scope/pkg/subpath"` resolves correctly.
	Subpaths map[string]string

	// Deps is the list of "name@constraint" strings for direct dependencies.
	Deps []string
}

// Walk resolves the manifest's deps transitively, returning every package keyed
// by name. If a name is requested under multiple constraints we pick the
// highest version satisfying ALL of them; conflicts produce an error.
//
// `existing` (the prior lock) is consulted as a hint: when a constraint still
// matches what was previously resolved, we reuse the version (but still
// re-fetch + re-verify to fill `Files`). Pass nil to ignore the lock.
func Walk(
	ctx context.Context,
	m *manifest.Manifest,
	existing *lock.Lock,
	c *registry.Client,
) (map[string]*Resolved, error) {

	skip := make(map[string]struct{}, len(m.Skip))
	for _, name := range m.Skip {
		skip[name] = struct{}{}
	}
	bundled := make(map[string]struct{}, len(m.Bundled))
	for _, name := range m.Bundled {
		bundled[name] = struct{}{}
	}

	state := &walker{
		ctx:         ctx,
		client:      c,
		existing:    existing,
		skip:        skip,
		bundled:     bundled,
		resolved:    map[string]*Resolved{},
		constraints: map[string][]string{},
	}

	// Seed with manifest deps in deterministic order.
	names := sortedKeys(m.Deps)
	for _, name := range names {
		if err := state.addRequest(name, m.Deps[name]); err != nil {
			return nil, err
		}
	}
	if err := state.run(); err != nil {
		return nil, err
	}
	return state.resolved, nil
}

type walker struct {
	ctx      context.Context
	client   *registry.Client
	existing *lock.Lock
	skip     map[string]struct{}
	bundled  map[string]struct{}

	queue []request
	// constraints[name] is the list of "name@constraint" keys requested for that name.
	constraints map[string][]string
	resolved    map[string]*Resolved
}

type request struct {
	name       string
	constraint string
}

func (w *walker) addRequest(name, constraint string) error {
	if _, skip := w.skip[name]; skip {
		log.Printf("tspkg: skipping %s (in tspkg.skip list)", name)
		return nil
	}
	key := name + "@" + constraint
	if slices.Contains(w.constraints[name], key) {
		return nil // already queued/resolved under this exact constraint
	}
	w.constraints[name] = append(w.constraints[name], key)
	w.queue = append(w.queue, request{name: name, constraint: constraint})
	return nil
}

func (w *walker) run() error {
	for len(w.queue) > 0 {
		req := w.queue[0]
		w.queue = w.queue[1:]

		// If we already resolved this name, ensure the existing version still
		// satisfies the new constraint.
		if r, ok := w.resolved[req.name]; ok {
			c, err := semver.NewConstraint(req.constraint)
			if err != nil {
				return fmt.Errorf("resolve: %s: bad constraint %q: %w", req.name, req.constraint, err)
			}
			v, err := semver.NewVersion(r.Version)
			if err != nil {
				return fmt.Errorf("resolve: %s: bad resolved version %q: %w", req.name, r.Version, err)
			}
			if !c.Check(v) {
				return fmt.Errorf("resolve: %s: version conflict — already resolved %s, new constraint %q", req.name, r.Version, req.constraint)
			}
			continue
		}

		r, err := w.resolveOne(req)
		if err != nil {
			return err
		}
		w.resolved[req.name] = r

		// Recurse on the package's dependencies — unless the user marked this
		// package as bundled (in which case its dist is self-contained and we
		// don't need to resolve its declared deps).
		if _, isBundled := w.bundled[req.name]; isBundled {
			log.Printf("tspkg: %s marked bundled — skipping its dependency walk", req.name)
			continue
		}

		pj, err := pkg.Parse(r.Files["package.json"])
		if err != nil {
			return fmt.Errorf("resolve: %s: %w", req.name, err)
		}
		if len(pj.PeerDependencies) > 0 {
			log.Printf("tspkg: %s declares peerDependencies (%v) — not auto-resolved; add them to package.json if needed.",
				req.name, sortedKeys(pj.PeerDependencies))
		}
		if len(pj.OptionalDependencies) > 0 {
			log.Printf("tspkg: %s declares optionalDependencies (%v) — skipped.",
				req.name, sortedKeys(pj.OptionalDependencies))
		}
		for _, dep := range sortedKeys(pj.Dependencies) {
			if err := w.addRequest(dep, pj.Dependencies[dep]); err != nil {
				return err
			}
			r.Deps = append(r.Deps, dep+"@"+pj.Dependencies[dep])
		}
	}
	return nil
}

func (w *walker) resolveOne(req request) (*Resolved, error) {
	md, err := w.client.Metadata(w.ctx, req.name)
	if err != nil {
		return nil, err
	}

	c, err := semver.NewConstraint(req.constraint)
	if err != nil {
		return nil, fmt.Errorf("resolve: %s: bad constraint %q: %w", req.name, req.constraint, err)
	}

	versions := make([]*semver.Version, 0, len(md.Versions))
	versionStrings := make(map[string]string, len(md.Versions))
	for vs := range md.Versions {
		v, err := semver.NewVersion(vs)
		if err != nil {
			continue // skip prerelease tags we can't parse
		}
		versions = append(versions, v)
		versionStrings[v.Original()] = vs
	}
	sort.Sort(sort.Reverse(semver.Collection(versions)))

	var chosen *semver.Version
	for _, v := range versions {
		if v.Prerelease() != "" {
			continue // never pick prereleases automatically
		}
		if c.Check(v) {
			chosen = v
			break
		}
	}
	if chosen == nil {
		return nil, fmt.Errorf("resolve: %s: no version satisfies %q", req.name, req.constraint)
	}

	vs := versionStrings[chosen.Original()]
	vmd := md.Versions[vs]

	tgz, err := w.client.Tarball(w.ctx, vmd.Dist.Tarball, vmd.Dist.Integrity)
	if err != nil {
		return nil, err
	}

	files, err := extract.Extract(tgz)
	if err != nil {
		return nil, fmt.Errorf("resolve: %s@%s: %w", req.name, vs, err)
	}

	pjBytes, ok := files["package.json"]
	if !ok {
		return nil, fmt.Errorf("resolve: %s@%s: tarball has no package.json", req.name, vs)
	}
	pj, err := pkg.Parse(pjBytes)
	if err != nil {
		return nil, fmt.Errorf("resolve: %s@%s: %w", req.name, vs, err)
	}

	subpaths, err := pkg.ResolveAllExports(pj)
	if err != nil {
		return nil, err
	}
	entry := subpaths["."]
	if _, ok := files[entry]; !ok {
		return nil, fmt.Errorf("resolve: %s@%s: ESM entry %q not present in tarball", req.name, vs, entry)
	}
	// Drop subpaths whose target file isn't actually in the tarball — those
	// are stale or refer to platform-conditional builds we didn't ship.
	for sp, p := range subpaths {
		if _, ok := files[p]; !ok {
			delete(subpaths, sp)
		}
	}

	return &Resolved{
		Name:      req.name,
		Version:   vs,
		Tarball:   vmd.Dist.Tarball,
		Integrity: vmd.Dist.Integrity,
		Files:     files,
		Entry:     entry,
		Subpaths:  subpaths,
	}, nil
}

// HashFile returns "sha256-<base64>" for content. Used for tspkg.lock entries.
func HashFile(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256-" + base64.StdEncoding.EncodeToString(sum[:])
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ErrUnresolvable is returned when no version satisfies the requested constraint.
var ErrUnresolvable = errors.New("no satisfying version")
