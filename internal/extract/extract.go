// Package extract unpacks an npm tarball (gzipped tar with a "package/" root
// prefix) into an in-memory map of relative paths to bytes.
package extract

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
)

// Files is the unpacked contents of a tarball, keyed by path relative to the
// package root (the "package/" prefix is stripped).
type Files map[string][]byte

// Extract unpacks tgz, stripping the leading "package/" path component.
// Symlinks, hardlinks, devices, and absolute or escaping paths are rejected.
func Extract(tgz []byte) (Files, error) {
	gz, err := gzip.NewReader(bytes.NewReader(tgz))
	if err != nil {
		return nil, fmt.Errorf("extract: gzip: %w", err)
	}
	defer gz.Close()

	out := Files{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("extract: tar: %w", err)
		}

		switch hdr.Typeflag {
		case tar.TypeReg:
		case tar.TypeDir, tar.TypeXGlobalHeader:
			continue
		default:
			// Symlinks etc. are rejected — npm packages we accept don't use them.
			return nil, fmt.Errorf("extract: unexpected tar entry type %c for %q", hdr.Typeflag, hdr.Name)
		}

		name := stripPackagePrefix(hdr.Name)
		if name == "" {
			continue
		}
		if strings.HasPrefix(name, "/") || strings.Contains(name, "..") {
			return nil, fmt.Errorf("extract: unsafe path %q", hdr.Name)
		}

		buf, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("extract: read %q: %w", hdr.Name, err)
		}
		out[name] = buf
	}
	return out, nil
}

// stripPackagePrefix removes the leading directory (almost always "package/")
// that npm tarballs are wrapped in. Returns "" for entries that are only the
// prefix itself.
func stripPackagePrefix(p string) string {
	p = strings.TrimPrefix(p, "./")
	_, after, ok := strings.Cut(p, "/")
	if !ok {
		return ""
	}
	return after
}
