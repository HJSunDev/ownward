package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// OrganizationExecutionIdentity identifies one source revision and work item,
// independently of temporary execution leases and transport sessions.
func OrganizationExecutionIdentity(id string, revision uint64, generation, work string) string {
	data, _ := json.Marshal([]any{id, revision, generation, work})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
