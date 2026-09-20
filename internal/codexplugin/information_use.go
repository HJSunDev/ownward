package codexplugin

import _ "embed"

// InformationUseInstructions is delivered in the existing host context. Routing
// and sufficiency remain agent decisions; no classifier or separate agent runs.
// Its source contracts are checked against the portable information-use module.
//
//go:embed information_use.txt
var InformationUseInstructions string
