// Package registry talks to the public npm registry.
package registry

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultBase = "https://registry.npmjs.org"
	userAgent   = "tspkg/0.1"
)

// Client is a minimal npm registry HTTP client.
type Client struct {
	HTTP *http.Client
	Base string
}

// New returns a Client with sane defaults.
func New() *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 60 * time.Second},
		Base: DefaultBase,
	}
}

// PackageMetadata is the metadata document at /<pkg>.
type PackageMetadata struct {
	Name     string                     `json:"name"`
	DistTags map[string]string          `json:"dist-tags"`
	Versions map[string]VersionMetadata `json:"versions"`
}

// VersionMetadata is one entry in PackageMetadata.Versions. It mirrors the
// fields of the package's own package.json that we care about.
type VersionMetadata struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Type                 string            `json:"type,omitempty"`
	Main                 string            `json:"main,omitempty"`
	Module               string            `json:"module,omitempty"`
	Exports              json.RawMessage   `json:"exports,omitempty"`
	Dist                 Dist              `json:"dist"`
	Dependencies         map[string]string `json:"dependencies,omitempty"`
	PeerDependencies     map[string]string `json:"peerDependencies,omitempty"`
	OptionalDependencies map[string]string `json:"optionalDependencies,omitempty"`
}

// Dist holds the tarball location and integrity hash for a version.
type Dist struct {
	Tarball   string `json:"tarball"`
	Integrity string `json:"integrity"` // SRI: "sha512-<base64>"
	Shasum    string `json:"shasum"`
}

// Metadata fetches the registry document for a package name. Scoped names
// like "@scope/pkg" are URL-escaped (the slash becomes %2F).
func (c *Client) Metadata(ctx context.Context, pkg string) (*PackageMetadata, error) {
	u := c.Base + "/" + url.PathEscape(pkg)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry: GET %s: %w", u, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry: GET %s: status %d", u, resp.StatusCode)
	}

	var md PackageMetadata
	if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
		return nil, fmt.Errorf("registry: decode %s: %w", u, err)
	}
	return &md, nil
}

// Tarball downloads the tgz at url and verifies it against the SRI integrity
// string from the registry.
func (c *Client) Tarball(ctx context.Context, tgzURL, integrity string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tgzURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("registry: GET %s: %w", tgzURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry: GET %s: status %d", tgzURL, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("registry: read %s: %w", tgzURL, err)
	}

	if err := VerifyIntegrity(body, integrity); err != nil {
		return nil, fmt.Errorf("registry: %s: %w", tgzURL, err)
	}
	return body, nil
}

// VerifyIntegrity checks data against an SRI hash like "sha512-<base64>".
// Only sha512 is supported; that's what the npm registry serves today.
func VerifyIntegrity(data []byte, integrity string) error {
	const prefix = "sha512-"
	if !strings.HasPrefix(integrity, prefix) {
		return fmt.Errorf("integrity: only sha512 supported, got %q", integrity)
	}
	want, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(integrity, prefix))
	if err != nil {
		return fmt.Errorf("integrity: decode: %w", err)
	}
	sum := sha512.Sum512(data)
	if !bytesEqual(want, sum[:]) {
		return fmt.Errorf("integrity: hash mismatch")
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
