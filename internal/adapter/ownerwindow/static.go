package ownerwindow

import _ "embed"

// The five product surfaces ship in unit three. This small, embedded entry is
// the actual first-verification path and has no external asset dependencies.
//
//go:embed static/index.html
var landing []byte

//go:embed static/bootstrap.js
var bootstrapScript []byte
