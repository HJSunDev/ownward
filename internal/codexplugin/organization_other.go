//go:build !windows

package codexplugin

import (
	"errors"
	"os/exec"
)

type organizationLifetime struct{}

func OrganizationCapacity(OrganizationProfile) error {
	return errors.New("当前平台未提供后台组织资源保护；保留原路径")
}

func configureOrganizationProcess(*exec.Cmd) {}
func attachOrganizationProcess(*exec.Cmd, int) (*organizationLifetime, error) {
	return nil, errors.New("当前发布的后台组织进程资源保护仅支持Windows；保留原组织路径")
}
func (*organizationLifetime) close()                      {}
func (*organizationLifetime) resources() (uint64, uint32) { return 0, 0 }
