package boundedstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Only authoritative inputs participate in the source seal. Unselected derived
// generations are retired files, never alternative authorities.
func legacySources(ctx context.Context, root string) (map[string]string, error) {
	names := []string{"assets/information.jsonl", "authority/control.json", "state/current.json"}
	var p struct {
		Schema     string `json:"schema"`
		Generation string `json:"generation"`
		Digest     string `json:"manifest_sha256"`
	}
	e := readSmallJSON(filepath.Join(root, "state", "current.json"), &p)
	if e == nil {
		if p.Schema != "ownward.derived-current/v1" || p.Generation == "" || strings.ContainsAny(p.Generation, "/\\:") || p.Generation == "." || p.Generation == ".." {
			return nil, errors.New("旧派生世代指针无效")
		}
		manifest := "state/generations/" + p.Generation + "/manifest.json"
		d, e := sourceDigest(ctx, filepath.Join(root, filepath.FromSlash(manifest)))
		if e != nil || d != p.Digest {
			return nil, errors.New("旧派生清单校验失败")
		}
		names = append(names, manifest, "state/generations/"+p.Generation+"/organization.binlog")
	} else if errors.Is(e, os.ErrNotExist) {
		names = append(names, "state/organization.binlog", "state/organization.jsonl")
	} else {
		return nil, e
	}
	out := map[string]string{}
	for _, name := range names {
		d, e := sourceDigest(ctx, filepath.Join(root, filepath.FromSlash(name)))
		if e != nil {
			return nil, e
		}
		out[name] = d
	}
	return out, nil
}

func validateSourceNames(sources map[string]string) error {
	for name, digest := range sources {
		allowed := name == "assets/information.jsonl" || name == "authority/control.json" || name == "state/current.json" || name == "state/organization.binlog" || name == "state/organization.jsonl"
		parts := strings.Split(name, "/")
		if len(parts) == 4 && parts[0] == "state" && parts[1] == "generations" && parts[2] != "" && parts[2] != "." && parts[2] != ".." && (parts[3] == "manifest.json" || parts[3] == "organization.binlog") {
			allowed = true
		}
		if !allowed || strings.ContainsAny(name, "\\:") || (digest != "absent" && len(digest) != 64) {
			return fmt.Errorf("迁移来源路径无效: %q", name)
		}
	}
	return nil
}

func cleanLegacy(root string, sources map[string]string) error {
	if e := validateSourceNames(sources); e != nil {
		return e
	}
	// The original lock and rejection marker remain at their original names.
	for _, name := range []string{"assets/information.jsonl", "authority/control.json", "state"} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if e := rejectSymlinkTree(path); e != nil {
			return e
		}
		if e := os.RemoveAll(path); e != nil {
			return e
		}
	}
	return nil
}

func rejectSymlinkTree(path string) error {
	return filepath.WalkDir(path, func(_ string, d os.DirEntry, e error) error {
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("迁移目录不得包含链接")
		}
		return nil
	})
}
