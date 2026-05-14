// tspkg vendors npm packages directly from the registry — no Node, no bun.
// Accepts ESM-shipping packages only.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"go.flaticols.dev/tspkg/internal/genimportmap"
	"go.flaticols.dev/tspkg/internal/lock"
	"go.flaticols.dev/tspkg/internal/manifest"
	"go.flaticols.dev/tspkg/internal/place"
	"go.flaticols.dev/tspkg/internal/registry"
	"go.flaticols.dev/tspkg/internal/resolve"
)

const manifestPath = "package.json"

func main() {
	log.SetFlags(0)
	log.SetPrefix("tspkg: ")

	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	cmd := os.Args[1]
	switch cmd {
	case "sync":
		mustOK(cmdSync(context.Background()))
	case "verify":
		mustOK(cmdVerify())
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "tspkg: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w *os.File) {
	fmt.Fprintf(w, `tspkg — vendor ESM npm packages from the registry.

Usage:
  go tool tspkg <command>

Commands:
  sync     Resolve manifest deps, fetch + verify tarballs, vendor files,
           write tspkg.lock, regenerate the importmap.
  verify   Recompute file hashes and check them against tspkg.lock.
  help     Show this help.

Manifest is package.json (standard "dependencies" field). Per-package and
per-project config lives under the namespaced "tspkg" object:

  - include      glob-list per package, selecting files to vendor
  - skip         transitive deps to ignore
  - bundled      packages whose dist is self-contained
  - groups       buckets package names into named importmap groups
  - paths        overrides for vendor/, lock, importmap output, URL base
  - importmap    overrides for the generated file's Go package name

Defaults match the conventional layout: vendor → assets/static/vendor,
lock → tspkg.lock, importmap → importmap/importmap.go (package importmap),
vendor URL base → /static/vendor.

Only ESM-shipping packages are accepted. CJS-only or build-required packages
are rejected with a clear error.
`)
}

func cmdSync(ctx context.Context) error {
	m, err := manifest.Read(manifestPath)
	if err != nil {
		return err
	}

	prev, err := lock.Read(m.Paths.Lock)
	if err != nil {
		return err
	}

	client := registry.New()
	resolved, err := resolve.Walk(ctx, m, prev, client)
	if err != nil {
		return err
	}
	log.Printf("resolved %d packages", len(resolved))

	hashes, err := place.WriteAll(m.Paths.Vendor, m, resolved)
	if err != nil {
		return err
	}
	log.Printf("vendored to %s/", m.Paths.Vendor)

	if err := genimportmap.Generate(resolved, m.Paths.VendorURLBase, m.Paths.Importmap, m.Importmap.Package, m.Groups); err != nil {
		return err
	}
	log.Printf("wrote %s", m.Paths.Importmap)

	newLock := &lock.Lock{
		Version:  lock.CurrentVersion,
		Resolved: map[string]lock.ResolvedEntry{},
	}
	for name, r := range resolved {
		// Find the manifest constraint (or "transitive" marker) for this name.
		key := name + "@" + constraintFor(name, m, r)
		newLock.Resolved[key] = lock.ResolvedEntry{
			Version:   r.Version,
			Tarball:   r.Tarball,
			Integrity: r.Integrity,
			Files:     hashes[name],
			Deps:      r.Deps,
		}
	}
	if err := lock.Write(newLock, m.Paths.Lock); err != nil {
		return err
	}
	log.Printf("wrote %s", m.Paths.Lock)
	return nil
}

// constraintFor returns the manifest-declared constraint for `name`, or
// "transitive" when the dep was pulled in via another package.
func constraintFor(name string, m *manifest.Manifest, _ *resolve.Resolved) string {
	if c, ok := m.Deps[name]; ok {
		return c
	}
	return "transitive"
}

func cmdVerify() error {
	m, err := manifest.Read(manifestPath)
	if err != nil {
		return err
	}
	l, err := lock.Read(m.Paths.Lock)
	if err != nil {
		return err
	}
	if len(l.Resolved) == 0 {
		return fmt.Errorf("verify: %s is empty — run `tspkg sync` first", m.Paths.Lock)
	}

	expected := make(map[string]map[string]string, len(l.Resolved))
	for key, entry := range l.Resolved {
		name := stripConstraint(key)
		expected[name] = entry.Files
	}

	bad, err := place.Verify(m.Paths.Vendor, expected)
	if err != nil {
		return err
	}
	if len(bad) > 0 {
		for _, b := range bad {
			log.Println(b)
		}
		return fmt.Errorf("verify: %d file(s) failed integrity check", len(bad))
	}
	log.Printf("verified %d packages, all hashes match", len(expected))
	return nil
}

// stripConstraint returns "name" from "name@constraint" — including for scoped
// packages like "@phosphor-icons/core@^2.1.0".
func stripConstraint(key string) string {
	// Walk from the end to find the last '@' that isn't at position 0 (which
	// would be a scoped-package marker).
	for i := len(key) - 1; i > 0; i-- {
		if key[i] == '@' {
			return key[:i]
		}
	}
	return key
}

func mustOK(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
