package musereelsdk

import "github.com/emiya-dev/musereel-sdk/jcs"

// CanonicalizeJSON exposes the independent JCS package at the SDK root for
// callers constructing request fingerprints or equivalent signed content.
func CanonicalizeJSON(raw []byte) (string, error) {
	return jcs.CanonicalizeJSON(raw)
}
