// Package importmap provides a small helper for consumers of tspkg-generated
// import maps. tspkg writes a Go source file (default path
// "importmap/importmap.go") into the consumer's tree, declaring one
// `const <Group>` string per group of vendored packages. Those strings use a
// canonical "/static/" URL prefix; consumers serving the vendored tree under
// a different prefix call Rebase at render time to rewrite the URLs onto
// their chosen asset base.
package importmap

import "strings"

// CanonicalPrefix is the URL prefix used in tspkg-generated import-map JSON.
// Rebase rewrites this to a consumer-chosen base.
const CanonicalPrefix = "/static/"

// Rebase returns json with the canonical "/static/" prefix rewritten onto
// assetBase. Pass "/static/" (or the empty string) to get the JSON unchanged.
func Rebase(assetBase, json string) string {
	if assetBase == "" || assetBase == CanonicalPrefix {
		return json
	}
	if !strings.HasSuffix(assetBase, "/") {
		assetBase += "/"
	}
	return strings.ReplaceAll(json, CanonicalPrefix, assetBase)
}
