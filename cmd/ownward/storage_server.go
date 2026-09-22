package main

import (
	"github.com/HJSunDev/ownward/internal/adapter/mcpserver"
	"github.com/HJSunDev/ownward/internal/assembly"
	"github.com/HJSunDev/ownward/internal/ownerview"
)

func productServer(r *assembly.Runtime) httpMCPServer {
	if s := r.Streaming(); s != nil {
		server := mcpserver.NewStreamingStorage(s, version, s.Scratch, s.Budget, s.DiskBytes)
		server.AddManagementTools(r.Management())
		view, err := ownerview.New(s.Store, r.UserControl(), r.Management())
		if err != nil {
			panic(err)
		} // operating-system entropy failure, before serving
		server.AddDraftTools(view)
		return server
	}
	return mcpserver.New(r.Product(), version)
}
