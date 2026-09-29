package embedding

import (
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

type runtimeBuildProfile struct {
	Archive, Entry, OS, Arch string
	Files                    map[string]struct{}
	Digests                  map[string]string
}

// 构建记录属于显式发行输入；安装后仍由封存组合和内容摘要校验，不能绕过运行验证。
func runtimeProfile(path string) (runtimeBuildProfile, error) {
	if path == "" {
		return runtimeBuildProfile{Archive: SelectedRuntimeArchiveSHA256, Entry: selectedRuntimeEntry, Files: selectedRuntimeFiles}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return runtimeBuildProfile{}, err
	}
	defer f.Close()
	var receipt struct {
		Schema  string            `json:"schema"`
		Source  string            `json:"source_sha256"`
		Patch   string            `json:"patch"`
		OS      string            `json:"os"`
		Arch    string            `json:"arch"`
		Entry   string            `json:"entry"`
		Archive string            `json:"runtime_archive_sha256"`
		Files   map[string]string `json:"files"`
	}
	d := json.NewDecoder(io.LimitReader(f, 16384))
	d.DisallowUnknownFields()
	if err = d.Decode(&receipt); err != nil {
		return runtimeBuildProfile{}, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return runtimeBuildProfile{}, errors.New("运行时构建记录有多余内容")
	}
	if receipt.Schema != "ownward.runtime-build/v1" || receipt.Source != "b83890db3d902d4c49d5ee638bade9967011beca56bbd803173689f615c2a9c3" || receipt.Patch != "embedding-output-allocation-v1" || (receipt.OS != "linux" && receipt.OS != "darwin") || (receipt.Arch != "amd64" && receipt.Arch != "arm64") || receipt.Entry != "llama-server" || !validDigest(receipt.Archive) || len(receipt.Files) != 1 || !validDigest(receipt.Files[receipt.Entry]) {
		return runtimeBuildProfile{}, errors.New("运行时构建记录不符合锁定源码与平台契约")
	}
	return runtimeBuildProfile{Archive: receipt.Archive, Entry: "runtime/" + receipt.Entry, OS: receipt.OS, Arch: receipt.Arch, Files: map[string]struct{}{receipt.Entry: {}}, Digests: receipt.Files}, nil
}

// 从可执行文件核对平台与架构，防止新平台误带旧 Windows 运行资源。
func VerifyRuntimeTarget(bundle Bundle, targetOS, targetArch string) error {
	osName, arch := bundle.Manifest.Runtime.OS, bundle.Manifest.Runtime.Arch
	if osName == "" && arch == "" {
		osName, arch = "windows", "amd64"
	}
	if osName != targetOS || arch != targetArch {
		return fmt.Errorf("向量运行时适用于 %s/%s，发布目标为 %s/%s", osName, arch, targetOS, targetArch)
	}
	match := false
	switch targetOS {
	case "windows":
		f, err := pe.Open(bundle.RuntimePath)
		if err != nil {
			return err
		}
		defer f.Close()
		match = (arch == "amd64" && f.Machine == pe.IMAGE_FILE_MACHINE_AMD64) || (arch == "arm64" && f.Machine == pe.IMAGE_FILE_MACHINE_ARM64)
	case "linux":
		f, err := elf.Open(bundle.RuntimePath)
		if err != nil {
			return err
		}
		defer f.Close()
		match = (arch == "amd64" && f.Machine == elf.EM_X86_64) || (arch == "arm64" && f.Machine == elf.EM_AARCH64)
	case "darwin":
		f, err := macho.Open(bundle.RuntimePath)
		if err != nil {
			return err
		}
		defer f.Close()
		match = (arch == "amd64" && f.Cpu == macho.CpuAmd64) || (arch == "arm64" && f.Cpu == macho.CpuArm64)
	}
	if !match {
		return errors.New("向量运行程序与声明的处理器架构不一致")
	}
	return nil
}
