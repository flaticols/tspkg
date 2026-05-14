// Package pkg parses a package.json from inside a downloaded package and
// resolves its ESM entry point. This is where the ESM-only gate lives.
package pkg

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PkgJSON is the subset of fields tspkg cares about.
type PkgJSON struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Type                 string            `json:"type,omitempty"`
	Main                 string            `json:"main,omitempty"`
	Module               string            `json:"module,omitempty"`
	Exports              json.RawMessage   `json:"exports,omitempty"`
	Dependencies         map[string]string `json:"dependencies,omitempty"`
	PeerDependencies     map[string]string `json:"peerDependencies,omitempty"`
	OptionalDependencies map[string]string `json:"optionalDependencies,omitempty"`
}

// Parse decodes a package.json byte slice.
func Parse(data []byte) (*PkgJSON, error) {
	var p PkgJSON
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("pkg: parse: %w", err)
	}
	return &p, nil
}

// ResolveAllExports returns every ESM-resolvable subpath in `exports`, keyed
// by the subpath ("." for the package root, "./dom", "./settings", etc.) with
// the package-relative file path as the value. Non-ESM and non-JS subpaths
// (like "./package.json") are skipped silently.
//
// Falls back to ResolveESMEntry for the "." key when `exports` is missing.
func ResolveAllExports(p *PkgJSON) (map[string]string, error) {
	out := map[string]string{}

	// Always try to resolve the root entry the regular way; any error here is
	// fatal because consumers expect at least the bare specifier to work.
	root, err := ResolveESMEntry(p)
	if err != nil {
		return nil, err
	}
	out["."] = root

	if len(p.Exports) == 0 {
		return out, nil
	}

	// Only object-form exports can have subpath keys.
	var asObj map[string]json.RawMessage
	if err := json.Unmarshal(p.Exports, &asObj); err != nil {
		return out, nil
	}

	for key, raw := range asObj {
		if key == "." || !strings.HasPrefix(key, "./") {
			continue
		}
		if strings.HasSuffix(key, ".json") {
			continue
		}
		if path, ok := resolveConditions(raw, p, false); ok {
			out[key] = cleanRel(path)
		}
		// Subpaths that don't resolve to ESM are silently skipped; they're
		// optional surface, and most callers will never import them.
	}
	return out, nil
}

// ResolveESMEntry returns the package-relative path of the ESM entry for the
// root specifier (i.e. `import "<name>"`).
//
// Resolution order, all ESM-only:
//  1. exports["."].import   (or exports["."].default for module-type pkgs)
//  2. exports["import"]      (when exports is a string)
//  3. module
//  4. main, only if it ends in .mjs OR (.js AND type == "module")
//
// Returns an error tagged with the package name if no usable ESM entry exists.
func ResolveESMEntry(p *PkgJSON) (string, error) {
	if path, ok := exportsRoot(p); ok {
		return cleanRel(path), nil
	}
	if p.Module != "" {
		return cleanRel(p.Module), nil
	}
	if p.Main != "" {
		if strings.HasSuffix(p.Main, ".mjs") || (p.Type == "module" && strings.HasSuffix(p.Main, ".js")) {
			return cleanRel(p.Main), nil
		}
	}
	return "", fmt.Errorf("pkg %q: no ESM entry (type=%q, main=%q, module=%q, exports=%s) — tspkg only accepts ESM-shipping packages",
		p.Name, p.Type, p.Main, p.Module, summarize(p.Exports))
}

// exportsRoot resolves package.json#exports to the ESM entry for the package
// root ("."). Returns ok=false if exports is missing or has no ESM entry for
// the root specifier.
//
// The boolean `viaESMCondition` argument tracks whether we got here through
// an ESM-asserting condition key (`module`, `browser`, `import`). Inside such
// a condition, plain `.js` files are accepted as ESM even when the package
// doesn't declare `type: module` — the condition itself is the assertion.
func exportsRoot(p *PkgJSON) (string, bool) {
	if len(p.Exports) == 0 {
		return "", false
	}

	// String shorthand: "exports": "./index.mjs"
	var asString string
	if err := json.Unmarshal(p.Exports, &asString); err == nil {
		if isESMPath(asString, p, false) {
			return asString, true
		}
		return "", false
	}

	// Object shape — could be either { ".": {...} } (subpath map) or a single
	// conditions map keyed by "import"/"default"/"require".
	var asObj map[string]json.RawMessage
	if err := json.Unmarshal(p.Exports, &asObj); err != nil {
		return "", false
	}

	if rootRaw, ok := asObj["."]; ok {
		return resolveConditions(rootRaw, p, false)
	}
	// No "." key — treat the whole object as a conditions map for the root.
	return resolveConditions(p.Exports, p, false)
}

// resolveConditions walks an exports value (string or object) for an ESM
// entry. viaESM is true when the caller already got here through an
// ESM-asserting condition (module/import/browser).
func resolveConditions(raw json.RawMessage, p *PkgJSON, viaESM bool) (string, bool) {
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		if isESMPath(asString, p, viaESM) {
			return asString, true
		}
		return "", false
	}

	var conds map[string]json.RawMessage
	if err := json.Unmarshal(raw, &conds); err != nil {
		return "", false
	}
	// Prefer "module" first — packages that ship both "module" (real ESM) and
	// "import" (often a CJS-to-ESM bridge re-exporting from a CJS file) put
	// the genuine ESM build under "module". Then "browser" (ESM build for
	// browsers), then "import", then "default" as a last resort.
	esmConditions := map[string]bool{"module": true, "browser": true, "import": true}
	for _, key := range []string{"module", "browser", "import", "default"} {
		if v, ok := conds[key]; ok {
			if path, ok := resolveConditions(v, p, viaESM || esmConditions[key]); ok {
				return path, true
			}
		}
	}
	return "", false
}

// isESMPath returns true if path looks like an ESM-loadable file. When
// viaESMCondition is true (we resolved via an ESM-asserting exports key like
// "module"), plain .js is accepted regardless of package.json#type.
func isESMPath(path string, p *PkgJSON, viaESMCondition bool) bool {
	if path == "" {
		return false
	}
	if strings.HasSuffix(path, ".mjs") {
		return true
	}
	if strings.HasSuffix(path, ".cjs") {
		return false
	}
	if strings.HasSuffix(path, ".js") {
		return viaESMCondition || p.Type == "module"
	}
	return false
}

func cleanRel(p string) string {
	return strings.TrimPrefix(p, "./")
}

func summarize(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "<none>"
	}
	const max = 80
	s := string(raw)
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
