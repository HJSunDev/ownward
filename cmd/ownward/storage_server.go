package main

import (
	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/assembly"
)

func productServer(r *assembly.Runtime) httpMCPServer {
	if s := r.Streaming(); s != nil {
		server := mcpserver.NewStreamingStorage(s, version, s.Scratch, s.Budget, s.DiskBytes)
		server.AddManagementTools(r.Management())
		return server
	}
	return mcpserver.New(r.Product(), version)
}
