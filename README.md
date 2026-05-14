# tspkg

> [!NOTE]
> **In active development.** `tspkg` is **not** a replacement for `npm`,
> `pnpm`, `yarn`, or `bun`. It targets a narrow set of use cases (see below)
> and intentionally rejects anything that needs transpilation, bundling, or
> a Node runtime. If you need a general-purpose package manager, use one.
>
> Originally extracted from the `weld` library (still in development and
> not yet public). `tspkg` is small and self-contained enough to stand on
> its own, so it's released ahead of the rest.

A small Go tool that vendors ESM-only npm packages **directly from the npm
registry** — no `node`, no `npm`, no `bun` required. Designed for projects
that ship browser code via HTML import maps and want their whole frontend
toolchain to live inside `go.mod`.

## Use cases

TBA

`tspkg` reads `package.json`, fetches and verifies tarballs over HTTPS,
unpacks them into a configurable vendor directory, generates a Go source
file with the import-map JSON as `const` strings (one per group), and
writes a lockfile with per-file SHA-256 hashes for offline integrity checks.

## Use

Register `tspkg` as a Go tool dependency:

```sh
go get -tool go.flaticols.dev/tspkg/cmd/tspkg
```

Then drive it from anywhere in the project:

```sh
go tool tspkg sync     # resolve, fetch + verify tarballs, vendor, regenerate importmap + lock
go tool tspkg verify   # recompute hashes for the vendored files; non-zero exit on mismatch
go tool tspkg help     # usage
```

Both commands read `package.json` from the current working directory.

## Manifest

`package.json` carries the dependency set under the standard `dependencies`
field and per-project config under a namespaced `tspkg` block (npm and other
tooling ignore unknown top-level fields, so this is forward-compatible).

```json
{
  "dependencies": {
    "@phosphor-icons/core": "^2.1.0",
    "graphology": "^0.26.0",
    "sigma": "^3.0.0"
  },
  "tspkg": {
    "include": {
      "@phosphor-icons/core": [
        "package.json",
        "assets/duotone/**/*.svg",
        "LICENSE*"
      ]
    },
    "skip":    ["events"],
    "bundled": ["graphology", "sigma"],
    "groups":  { "graph": ["sigma", "graphology"] },

    "paths": {
      "vendor":        "assets/static/vendor",
      "lock":          "tspkg.lock",
      "importmap":     "importmap/importmap.go",
      "vendorUrlBase": "/static/vendor"
    },
    "importmap": { "package": "importmap" }
  }
}
```

| Field | Effect |
| --- | --- |
| `dependencies` | Standard npm field — source of truth for which packages and version constraints. |
| `include[<pkg>]` | Doublestar globs (relative to package root) selecting files to vendor. When unset, `tspkg` vendors everything except obvious noise (`*.md` except `LICENSE`, `*.ts`, `*.map`, `test/`, `src/`, `docs/`, etc.). |
| `skip` | Names of transitive deps to ignore. Use for phantom CJS deps that bundled distributions don't actually import at runtime. You take responsibility for runtime correctness. |
| `bundled` | Names of packages whose dist is self-contained — `tspkg` vendors the package itself but doesn't walk its declared dependencies. Common for libraries that ship a bundled ESM build (sigma, graphology). |
| `groups[<name>]` | Buckets package names into named groups; each becomes a `const <Group>` in the generated Go file. Pages can inline only the maps they need. Packages not listed in any group land in the implicit default group (`const Base`). |
| `paths.vendor` | Directory to write vendored packages into. Default `assets/static/vendor`. |
| `paths.lock` | Lockfile path. Default `tspkg.lock`. |
| `paths.importmap` | Output path of the generated Go file. Default `importmap/importmap.go`. |
| `paths.vendorUrlBase` | URL prefix under which the vendored tree is served. Default `/static/vendor`. Must start with `/static/` so that [`importmap.Rebase`](#rebase) can rewrite onto a per-consumer asset base. |
| `importmap.package` | Go package name declared at the top of the generated file. Default `importmap`. |

## Outputs

- **Vendor directory** (`paths.vendor`) — the unpacked package files. Gitignore this; `sync` regenerates it.
- **Generated importmap** (`paths.importmap`) — a Go file that exports `const Base` plus one `const <Group>` per declared group, each containing the import-map JSON as a string. Commit this — it acts as a snapshot of what the live bundle looks like and is consumed by the templ layout.
- **Lockfile** (`paths.lock`) — resolved versions, SRI `sha512` integrity from the registry, and per-file `sha256` hashes used by `verify`. Commit this.

## ESM-only constraint

`tspkg` rejects packages that don't ship a usable ESM entry. The resolver
checks, in order:

1. `exports["."].module` (preferred — typically the genuine ESM build)
2. `exports["."].browser`
3. `exports["."].import`
4. `module`
5. `main`, only if it ends in `.mjs` OR (`.js` AND `"type": "module"`)

CJS-only or build-required packages produce a clear error rather than a
broken vendor tree.

## Consumer wiring

Render the generated import map from a templ layout (or any HTML template).
The provided `importmap.Rebase` helper rewrites the canonical `/static/`
URL prefix onto whatever path the consumer mounts their static handler at.

```go
// internal/views/layout.templ
package views

import (
    "yourapp/importmap" // the generated package
    tspkg "go.flaticols.dev/tspkg/importmap"
)

templ Layout(assetBase string) {
    <html>
        <head>
            @templ.Raw(`<script type="importmap">` + tspkg.Rebase(assetBase, importmap.Base) + `</script>`)
            // Optionally inline a heavier group only on pages that need it:
            // @templ.Raw(`<script type="importmap">` + tspkg.Rebase(assetBase, importmap.Graph) + `</script>`)
        </head>
        <body>{ children... }</body>
    </html>
}
```

## Rebase

Generated map URLs use the canonical `/static/` prefix. `importmap.Rebase`
swaps that prefix for the consumer's asset base — useful when serving the
vendor tree from a path other than `/static/`:

```go
tspkg.Rebase("/assets/", importmap.Base) // replaces "/static/" with "/assets/"
tspkg.Rebase("/static/", importmap.Base) // no-op
tspkg.Rebase("",         importmap.Base) // no-op
```

## Limitations (by design)

- ESM only. No CJS, no UMD, no transpilation, no bundling.
- Only `sha512` SRI hashes are recognized (the npm registry's current format).
- No npm authentication; only public registry packages.
- No lockfile-driven offline mode; `sync` always re-fetches.
- No watch mode; it's a one-shot build step.

## Roadmap

- TypeScript transpilation via [`typescript-go`](https://github.com/microsoft/typescript-go)
  is on the table — would let packages that ship `.ts` sources be vendored as
  usable `.js`. Not a priority right now; the current ESM-only flow covers the
  intended use cases.
