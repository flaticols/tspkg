// Package lock reads and writes tspkg.lock, the resolved-version + integrity
// record for the current vendored set.
package lock

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
)

// CurrentVersion is the lockfile schema version.
const CurrentVersion = 1

// Lock is the deserialized tspkg.lock.
type Lock struct {
	Version  int                      `json:"version"`
	Resolved map[string]ResolvedEntry `json:"resolved"`
}

// ResolvedEntry is one package's resolution + content fingerprint.
type ResolvedEntry struct {
	Version   string            `json:"version"`
	Tarball   string            `json:"tarball"`
	Integrity string            `json:"integrity"`
	Files     map[string]string `json:"files"` // path → "sha256-<base64>"
	Deps      []string          `json:"deps"`  // direct deps as "name@constraint"
}

// Read parses tspkg.lock at path. If the file is missing, returns an empty
// Lock (callers can sync from scratch).
func Read(path string) (*Lock, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return &Lock{Version: CurrentVersion, Resolved: map[string]ResolvedEntry{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock: read %s: %w", path, err)
	}
	var l Lock
	if err := json.Unmarshal(data, &l); err != nil {
		return nil, fmt.Errorf("lock: parse %s: %w", path, err)
	}
	if l.Version != CurrentVersion {
		return nil, fmt.Errorf("lock: %s: schema version %d, want %d", path, l.Version, CurrentVersion)
	}
	if l.Resolved == nil {
		l.Resolved = map[string]ResolvedEntry{}
	}
	return &l, nil
}

// Write atomically writes the lock to path with sorted keys for stable diffs.
func Write(l *Lock, path string) error {
	l.Version = CurrentVersion

	// Re-marshal with sorted keys at every level via a sorted re-encode.
	out := map[string]any{
		"version":  l.Version,
		"resolved": sortedResolved(l.Resolved),
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("lock: marshal: %w", err)
	}
	data = append(data, '\n')

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("lock: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("lock: rename %s → %s: %w", tmp, path, err)
	}
	return nil
}

// sortedResolved produces an ordered map representation so that JSON output is
// deterministic across runs (good for git diffs).
func sortedResolved(m map[string]ResolvedEntry) any {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	type orderedEntry struct {
		Key string
		Val ResolvedEntry
	}
	ordered := make([]orderedEntry, 0, len(keys))
	for _, k := range keys {
		v := m[k]
		// Sort entry's nested maps/slices too.
		if v.Files != nil {
			fileKeys := make([]string, 0, len(v.Files))
			for fk := range v.Files {
				fileKeys = append(fileKeys, fk)
			}
			sort.Strings(fileKeys)
			sortedFiles := make(map[string]string, len(v.Files))
			for _, fk := range fileKeys {
				sortedFiles[fk] = v.Files[fk]
			}
			v.Files = sortedFiles
		}
		sort.Strings(v.Deps)
		ordered = append(ordered, orderedEntry{Key: k, Val: v})
	}

	// json.Marshal of map[string]X iterates in sorted key order in Go ≥1.12,
	// so it's enough to ensure inner slices/maps are sorted and return a
	// rebuilt map.
	rebuilt := make(map[string]ResolvedEntry, len(ordered))
	for _, e := range ordered {
		rebuilt[e.Key] = e.Val
	}
	return rebuilt
}
