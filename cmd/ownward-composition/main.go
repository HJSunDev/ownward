package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/HJSunDev/ownward/internal/composition"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ownward-composition:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "verify"
	if len(args) > 0 && isCommand(args[0]) {
		command = args[0]
		args = args[1:]
	}
	switch command {
	case "verify", "seal":
		return runComposition(command, args)
	default:
		return fmt.Errorf("未知命令: %s", command)
	}
}

func isCommand(value string) bool {
	switch value {
	case "verify", "seal":
		return true
	default:
		return false
	}
}

func runComposition(command string, args []string) error {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	repository := flags.String("repository", ".", "仓库根目录")
	manifestPath := flags.String("manifest", filepath.FromSlash("manifests/compositions/v1/current-collaborative.json"), "组合清单")
	outputPath := flags.String("output", "", "seal 输出文件；为空时写标准输出")
	if err := flags.Parse(args); err != nil {
		return err
	}
	manifest, err := composition.Load(*manifestPath)
	if err != nil {
		return err
	}
	switch command {
	case "seal":
		sealed, err := composition.Seal(*repository, manifest)
		if err != nil {
			return err
		}
		if *outputPath != "" {
			output, err := os.Create(*outputPath)
			if err != nil {
				return err
			}
			if err := composition.WriteJSON(output, sealed); err != nil {
				_ = output.Close()
				return err
			}
			return output.Close()
		}
		return composition.WriteJSON(os.Stdout, sealed)
	case "verify":
		result, err := composition.Verify(*repository, manifest)
		if err != nil {
			return err
		}
		return composition.WriteJSON(os.Stdout, result)
	}
	return nil
}
