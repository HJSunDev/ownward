package boundedstore

import (
	"context"
	"encoding/json"

	"github.com/HJSunDev/ownward/internal/contract"
)

func (a *ControlAuthority) VisitPrincipals(ctx context.Context, visit func(contract.Principal) error) error {
	return a.store.view(ctx, func(q queryer) error {
		rows, e := q.QueryContext(ctx, "SELECT data FROM authority_items WHERE kind='principals' ORDER BY sequence")
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var data []byte
			if e = rows.Scan(&data); e != nil {
				return e
			}
			var p contract.Principal
			if e = json.Unmarshal(data, &p); e != nil {
				return e
			}
			p.CredentialDigest = ""
			if e = visit(p); e != nil {
				return e
			}
		}
		return rows.Err()
	})
}
