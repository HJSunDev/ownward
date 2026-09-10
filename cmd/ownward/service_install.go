package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/adapter/localowner"
	"github.com/HJSunDev/ownward/internal/adapter/remote"
	"github.com/HJSunDev/ownward/internal/assembly"
	"github.com/HJSunDev/ownward/internal/contract"
)

type installation struct {
	Root     string             `json:"root"`
	DataDir  string             `json:"data_dir"`
	Listen   string             `json:"listen"`
	Name     string             `json:"name"`
	Location contract.Location  `json:"location"`
	Source   *contract.Location `json:"source,omitempty"`
}

func (s installation) vault() localowner.Vault {
	return localowner.Vault{Root: filepath.Join(s.Root, "protected"), Machine: true}
}
func (s installation) save() error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return s.vault().Save("installation", "settings", string(data))
}
func loadInstallation(root string) (installation, remote.Identity, error) {
	var s installation
	root, err := filepath.Abs(root)
	if err != nil {
		return s, remote.Identity{}, err
	}
	v := localowner.Vault{Root: filepath.Join(root, "protected"), Machine: true}
	data, err := v.Load("installation", "settings")
	if err != nil {
		return s, remote.Identity{}, err
	}
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		return s, remote.Identity{}, err
	}
	var identity remote.Identity
	data, err = v.Load("installation", "identity")
	if err != nil {
		return s, identity, err
	}
	err = json.Unmarshal([]byte(data), &identity)
	return s, identity, err
}

func readMaterial(path string) (connectionMaterial, error) {
	var m connectionMaterial
	data, err := os.ReadFile(path)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if m.Location.ServiceID == "" {
		err = json.Unmarshal(data, &m.Location)
	}
	if err != nil {
		return m, err
	}
	return m, m.Location.Validate()
}

func runAccessCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("data-dir", "", "服务位置")
	connection := flags.String("connection", "", "可信公开连接材料")
	location := flags.String("location", "", "迁移后从可信管理入口取得的新公开位置；保留原连接身份")
	endpoint := flags.String("endpoint", "", "用户选择的可达 HTTPS 地址")
	listen := flags.String("listen", "", "此主机监听地址")
	source := flags.String("receive-from", "", "接收迁移：在目标主机认可的源公开连接材料")
	name := flags.String("name", "Ownward", "系统服务名称")
	output := flags.String("output", "", "公开连接材料保存位置")
	operation := flags.String("id", "", "待接入操作")
	marker := flags.String("marker", "", "目标宿主显示的核对标记")
	accept := flags.Bool("approve", false, "批准核对过的接入申请")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if args[0] == "connect" {
		m, err := readMaterial(*connection)
		if err != nil {
			return err
		}
		if *location != "" {
			next, err := readMaterial(*location)
			if err != nil {
				return err
			}
			if next.Location.SystemID != m.Location.SystemID {
				return errors.New("新位置不属于原信息体系")
			}
			m.Location = next.Location
		}
		return runRemoteConnector(ctx, m)
	}
	if *root == "" {
		return errors.New("请选择服务存放位置")
	}
	absolute, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	if args[0] == "service-install" {
		if *listen == "" {
			u, err := url.Parse(*endpoint)
			if err != nil {
				return err
			}
			port := u.Port()
			if port == "" {
				port = "443"
			}
			*listen = net.JoinHostPort("", port)
		}
		bundle, err := currentVectorBundleDirectory()
		if err != nil {
			return err
		}
		verified, err := assembly.PreflightSharedConnector(assembly.Collaborative, bundle)
		if err != nil {
			return err
		}
		if existing, _, err := loadInstallation(absolute); err == nil {
			if existing.Location.Endpoint != *endpoint || existing.Listen != *listen || existing.Name != *name || existing.Location.Composition != verified.Composition {
				return errors.New("该位置已有不同安装，请接续原安装参数")
			}
			if (existing.Source == nil) != (*source == "") {
				return errors.New("请接续原安装的部署模式")
			}
			if *source != "" {
				m, err := readMaterial(*source)
				if err != nil {
					return err
				}
				if m.Location != *existing.Source {
					return errors.New("请接续已绑定的迁移来源")
				}
			}
			if err := installOSService(existing); err != nil {
				return err
			}
			if *output != "" {
				if err := writePublic(*output, existing.Location); err != nil {
					return err
				}
			}
			return writeJSON(stdout, map[string]any{"status": "installed", "location": existing.Location, "message": "已有安装已接续；异地可达性取决于主机网络条件"})
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(absolute, 0700); err != nil {
			return err
		}
		if err := localowner.ProtectServiceDirectory(absolute); err != nil {
			return err
		}
		identity, err := remote.NewIdentity(*endpoint, verified.Composition)
		if err != nil {
			return err
		}
		s := installation{Root: absolute, DataDir: filepath.Join(absolute, "data"), Listen: *listen, Name: *name, Location: identity.Location}
		if _, err := os.Stat(filepath.Join(absolute, "authority", "control.json")); err == nil {
			s.DataDir = absolute
		}
		if *source != "" {
			if _, err := os.Stat(s.DataDir); !errors.Is(err, os.ErrNotExist) {
				return errors.New("接收迁移需要独立空位置，不能覆盖已有体系")
			}
			m, err := readMaterial(*source)
			if err != nil {
				return err
			}
			if m.Location.SystemID == "" || m.Location.Composition != s.Location.Composition {
				return errors.New("源体系与目的地发布组合不兼容")
			}
			s.Source = &m.Location
			s.Location.SystemID = m.Location.SystemID
		} else {
			runtime, err := assembly.Open(assembly.Request{DataDir: s.DataDir, ProductSemantics: assembly.Collaborative, VectorBundleDir: bundle})
			if err != nil {
				return err
			}
			var token string
			if runtime.UserControl().SystemID() == "" {
				token, err = runtime.UserControl().InitializeOwner("所有者")
			} else {
				token, err = runtime.UserControl().RecoverOwner()
			}
			if err == nil {
				s.Location.SystemID = runtime.UserControl().SystemID()
				err = s.vault().Save(s.Location.SystemID, "owner", token)
			}
			closeErr := runtime.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
		identity.Location = s.Location
		if err := s.vault().Save(ownerRecoveryScope(s.DataDir), "owner-recovery", connectionID()); err != nil {
			return err
		}
		data, err := json.Marshal(identity)
		if err != nil {
			return err
		}
		if err := s.vault().Save("installation", "identity", string(data)); err != nil {
			return err
		}
		if err := s.save(); err != nil {
			return err
		}
		// Probe local binding; actual remote reachability remains an explicit host/network condition.
		probe, err := net.Listen("tcp", s.Listen)
		if err != nil {
			return fmt.Errorf("主机尚不能监听所选入口: %w", err)
		}
		probe.Close()
		if err := installOSService(s); err != nil {
			return err
		}
		if *output != "" {
			if err := writePublic(*output, s.Location); err != nil {
				return err
			}
		}
		return writeJSON(stdout, map[string]any{"status": "installed", "location": s.Location, "message": "服务已安装；外部设备仍须能到达所选主机地址"})
	}
	s, identity, err := loadInstallation(absolute)
	if err != nil {
		return err
	}
	if args[0] == "service-run" {
		return runManagedService(ctx, s, identity)
	}
	if args[0] == "service-recover" {
		d, err := readSharedMCPDescriptor(filepath.Join(s.DataDir, "runtime", "mcp-service.json"))
		if err != nil {
			return err
		}
		proof, err := s.vault().Load(ownerRecoveryScope(s.DataDir), "owner-recovery")
		if err != nil {
			return err
		}
		h := hostConnector{descriptor: d}
		var result any
		if err := h.controlCall(ctx, "recover", proof, struct{}{}, &result); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"status": "ready", "message": "本机管理连接已恢复"})
	}
	client, err := remote.Client(s.Location, nil)
	if err != nil {
		return err
	}
	credential, err := s.vault().Load(s.Location.SystemID, "owner")
	if err != nil {
		return errors.New("部署主机管理入口尚未准备或需要恢复")
	}
	path := "/remote/enrollment/"
	var result any
	switch args[0] {
	case "service-invite":
		id := connectionID()
		var e contract.Enrollment
		if err := remoteCall(ctx, client, s.Location, path+"invite", credential, map[string]string{"id": id}, &e); err != nil {
			return err
		}
		m := connectionMaterial{Location: s.Location, EnrollmentID: id, Permissions: []contract.Permission{contract.ReadPermission, contract.MaintainPermission, contract.ManagePermission}}
		if *output != "" {
			if err := writePublic(*output, m); err != nil {
				return err
			}
		}
		result = m
	case "service-approve":
		if *operation == "" {
			var entries []contract.Enrollment
			if err := remoteCall(ctx, client, s.Location, path+"list", credential, struct{}{}, &entries); err != nil {
				return err
			}
			for _, e := range entries {
				if e.Status == "pending" {
					var p any
					if err := remoteCall(ctx, client, s.Location, path+"preview", credential, map[string]string{"id": e.ID}, &p); err != nil {
						return err
					}
					if err := writeJSON(stdout, p); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if strings.TrimSpace(*marker) == "" {
			return errors.New("请核对目标宿主的接入标记")
		}
		if err := remoteCall(ctx, client, s.Location, path+"decide", credential, map[string]any{"id": *operation, "marker": *marker, "accept": *accept}, &result); err != nil {
			return err
		}
	default:
		return errors.New("未知服务命令")
	}
	return writeJSON(stdout, result)
}

func writePublic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = file.Write(append(data, '\n'))
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	return closeErr
}
