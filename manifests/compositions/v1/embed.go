// Package compositionv1 exposes the exact source-verified composition sealed
// into each release binary. Runtime callers receive a copy and cannot mutate
// the embedded release identity.
package compositionv1

import _ "embed"

//go:embed current-collaborative.json
var currentCollaborative []byte

func CurrentCollaborative() []byte {
	return append([]byte(nil), currentCollaborative...)
}
