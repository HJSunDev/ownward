package semantics

import (
	_ "embed"
	"encoding/json"
)

// OrganizationContract is shared by host adapters, including the evaluation
// adapter. It contains no model, transport or task-specific behavior.
//
//go:embed organization_contract.json
var organizationContract []byte

func OrganizationInstruction() string {
	var contract struct {
		Instruction string `json:"instruction"`
	}
	if err := json.Unmarshal(organizationContract, &contract); err != nil {
		panic(err)
	}
	return contract.Instruction
}

// OrganizationOutputSchema is the same public submission shape that host
// adapters use when asking their own intelligence to organize information.
func OrganizationOutputSchema() json.RawMessage {
	var contract struct {
		OutputSchema json.RawMessage `json:"output_schema"`
	}
	if err := json.Unmarshal(organizationContract, &contract); err != nil {
		panic(err)
	}
	return contract.OutputSchema
}
