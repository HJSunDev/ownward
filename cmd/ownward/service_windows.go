//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/HJSunDev/ownward/internal/adapter/remote"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func installOSService(s installation) error {
	manager, err := mgr.Connect()
	if err != nil {
		return errors.New("安装系统服务需要部署主机管理员授权")
	}
	defer manager.Disconnect()
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	service, err := manager.CreateService(s.Name, binary, mgr.Config{DisplayName: "Ownward", Description: "用户持有的信息体系", StartType: mgr.StartAutomatic}, "service-run", "--data-dir", s.Root)
	if errors.Is(err, windows.ERROR_SERVICE_EXISTS) {
		service, err = manager.OpenService(s.Name)
		if err != nil {
			return err
		}
		config, configErr := service.Config()
		expected := windows.ComposeCommandLine([]string{binary, "service-run", "--data-dir", s.Root})
		if configErr != nil || config.BinaryPathName != expected {
			service.Close()
			return errors.New("同名系统服务不属于此安装，请选择独立服务名称")
		}
	}
	if err != nil {
		return err
	}
	defer service.Close()
	if err := service.SetRecoveryActions([]mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second}, {Type: mgr.ServiceRestart, Delay: 30 * time.Second}}, 86400); err != nil {
		return err
	}
	if err := service.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		state, err := service.Query()
		if err != nil {
			return err
		}
		if state.State == svc.Running {
			return nil
		}
		if state.State == svc.Stopped {
			return fmt.Errorf("系统服务启动失败（%d）；安装已保留，可重试", state.Win32ExitCode)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("安装已保留，系统服务仍在启动；请检查服务状态后接续")
}

type serviceRunner struct {
	s installation
	i remote.Identity
}

func (r serviceRunner) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status <- svc.Status{State: svc.StartPending}
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() { done <- serveInstallationReady(ctx, r.s, r.i, func() { close(ready) }) }()
	select {
	case err := <-done:
		if err != nil {
			return false, 1
		}
		return false, 0
	case <-ready:
	}
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			if err != nil {
				return false, 1
			}
			return false, 0
		case request := <-requests:
			switch request.Cmd {
			case svc.Interrogate:
				status <- request.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				cancel()
				if err := <-done; err != nil {
					return false, 1
				}
				return false, 0
			}
		}
	}
}
func runManagedService(ctx context.Context, s installation, i remote.Identity) error {
	managed, err := svc.IsWindowsService()
	if err != nil {
		return err
	}
	if managed {
		return svc.Run(s.Name, serviceRunner{s, i})
	}
	return serveInstallation(ctx, s, i)
}
