package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/HJSunDev/ownward/internal/composition"
	"github.com/HJSunDev/ownward/internal/contract"
	"github.com/HJSunDev/ownward/internal/kernelcatalog"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-kernel-version:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "verify"
	if len(args) > 0 && (args[0] == "verify" || args[0] == "seal") {
		command, args = args[0], args[1:]
	}
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	repository := flags.String("repository", ".", "仓库根目录")
	catalogPath := flags.String("catalog", filepath.FromSlash("manifests/kernel-generations/v1/catalog.json"), "内核世代目录")
	baselinePath := flags.String("baseline", filepath.FromSlash("benchmarks/acceptance/migration/v1/frozen-baseline.json"), "冻结迁移基线")
	outputPath := flags.String("output", "", "seal 输出文件；为空时写标准输出")
	if err := flags.Parse(args); err != nil {
		return err
	}
	catalog, err := kernelcatalog.Load(*catalogPath)
	if err != nil {
		return err
	}
	root, err := filepath.Abs(*repository)
	if err != nil {
		return err
	}
	read := func(generation kernelcatalog.Generation, path string) ([]byte, error) {
		if strings.TrimSpace(generation.Audit.SourceGit) == "" {
			return nil, fmt.Errorf("%s 缺少审计源码 revision", generation.Name)
		}
		sourcePath, selector, _ := strings.Cut(path, "#")
		output, commandErr := exec.Command("git", "-C", root, "show", generation.Audit.SourceGit+":"+filepath.ToSlash(sourcePath)).Output()
		if commandErr != nil {
			return nil, fmt.Errorf("读取 %s:%s: %w", generation.Audit.SourceGit, sourcePath, commandErr)
		}
		if selector != "" {
			return selectedGoContent(output, selector)
		}
		return output, nil
	}
	switch command {
	case "seal":
		contracts, err := authoritativeContracts(root)
		if err != nil {
			return err
		}
		for generationIndex := range catalog.Generations {
			bindContracts(&catalog.Generations[generationIndex].Kernel, contracts)
			for dependencyIndex := range catalog.Generations[generationIndex].Dependencies {
				bindContracts(&catalog.Generations[generationIndex].Dependencies[dependencyIndex], contracts)
			}
		}
		sealed, err := kernelcatalog.Seal(catalog, read)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*outputPath) != "" {
			output, err := os.Create(*outputPath)
			if err != nil {
				return err
			}
			if err := kernelcatalog.WriteJSON(output, sealed); err != nil {
				_ = output.Close()
				return err
			}
			return output.Close()
		}
		return kernelcatalog.WriteJSON(os.Stdout, sealed)
	case "verify":
		// The on-disk catalog is an immutable sealed input. Validate it before
		// loading or comparing any current authoritative definitions; verify
		// must never repair the object it is meant to inspect.
		verification, err := kernelcatalog.Verify(catalog)
		if err != nil {
			return err
		}
		contracts, err := authoritativeContracts(root)
		if err != nil {
			return err
		}
		if err := kernelcatalog.VerifyAuthoritativeContracts(catalog, contracts); err != nil {
			return err
		}
		sealed, err := kernelcatalog.Seal(catalog, read)
		if err != nil {
			return err
		}
		if sealed.Identity != catalog.Identity {
			return fmt.Errorf("审计源码内容与封存世代身份不一致")
		}
		if err := kernelcatalog.VerifyFrozenBaseline(root, *baselinePath, catalog); err != nil {
			return err
		}
		return kernelcatalog.WriteJSON(os.Stdout, verification)
	default:
		return fmt.Errorf("未知命令: %s", command)
	}
}

func authoritativeContracts(root string) (map[string][]contract.Reference, error) {
	manifest, err := composition.Load(filepath.Join(root, filepath.FromSlash("manifests/compositions/v1/current-collaborative.json")))
	if err != nil {
		return nil, err
	}
	if _, err := composition.Verify(root, manifest); err != nil {
		return nil, fmt.Errorf("核验权威组合契约: %w", err)
	}
	contracts := make(map[string][]contract.Reference, len(manifest.Components))
	for _, component := range manifest.Components {
		contracts[component.Role] = append([]contract.Reference(nil), component.Contracts...)
	}
	return contracts, nil
}

func selectedGoContent(source []byte, selector string) ([]byte, error) {
	const prefix = "go-const="
	if !strings.HasPrefix(selector, prefix) || strings.TrimPrefix(selector, prefix) == "" {
		return nil, fmt.Errorf("未知源码选择器: %s", selector)
	}
	wanted := strings.TrimPrefix(selector, prefix)
	file, err := parser.ParseFile(token.NewFileSet(), "source.go", source, 0)
	if err != nil {
		return nil, err
	}
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if !ok || general.Tok != token.CONST {
			continue
		}
		for _, specification := range general.Specs {
			value, ok := specification.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for index, name := range value.Names {
				if name.Name != wanted || index >= len(value.Values) {
					continue
				}
				literal, ok := value.Values[index].(*ast.BasicLit)
				if !ok {
					return nil, fmt.Errorf("Go 常量不是静态字面量: %s", wanted)
				}
				return []byte(literal.Value), nil
			}
		}
	}
	return nil, fmt.Errorf("Go 常量不存在: %s", wanted)
}

func bindContracts(component *composition.Component, byRole map[string][]contract.Reference) {
	component.Contracts = append([]contract.Reference(nil), byRole[component.Role]...)
}
