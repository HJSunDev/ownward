package contract

import (
	"context"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AccessAdapter maps one external protocol to the unified product capability.
// Authorization and product decisions remain outside the adapter.
type AccessAdapter interface {
	Run(context.Context, mcp.Transport) error
	HTTPHandler() http.Handler
}
