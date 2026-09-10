//go:build !windows

package main

import (
	"context"
	"errors"
	"github.com/HJSunDev/ownward/internal/adapter/remote"
)

func installOSService(installation) error {
	return errors.New("首个系统托管发布入口支持 Windows")
}
func runManagedService(ctx context.Context, s installation, i remote.Identity) error {
	return serveInstallation(ctx, s, i)
}
