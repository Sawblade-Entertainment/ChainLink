package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

type PackageInfo struct {
	Name    string
	Version string
}

type ChainLinkConfig struct {
	Package  PackageInfo
	Tools    map[string]string
	Packages map[string]string
}

func main() {
	// If we are called as a symlink for a tool, the first argument (os.Args[0]) 
	// will be the tool name.
	invokedAs := filepath.Base(os.Args[0])

	// If the binary name is NOT "chainlink", it means we were called via a symlink (e.g., "selene")
	if invokedAs != "chainlink" {
		// We are being invoked as a tool symlink
		if err := proxyToTool(invokedAs); err != nil {
			fmt.Printf("Tool %s not installed\n", invokedAs)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "install" {
		if err := InstallChainlink(); err != nil {
			fmt.Printf("Error installing Chainlink: %v\n", err)
			os.Exit(1)
		}
		return
	}

	var config ChainLinkConfig

	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		fmt.Printf("Error decoding config: %v\n", err)
		os.Exit(1)
	}

	if err := InstallToolList(config.Tools); err != nil {
		fmt.Printf("Error installing tools: %v\n", err)
		os.Exit(1)
	}

	if err := InstallPackageList(config.Packages); err != nil {
		fmt.Printf("Error installing packages: %v\n", err)
		os.Exit(1)
	}
}

func proxyToTool(toolName string) error {
	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		return fmt.Errorf("could not find chain.toml in current directory: %w", err)
	}

	spec, ok := config.Tools[toolName]
	if !ok {
		return fmt.Errorf("tool %s is not defined in chain.toml", toolName)
	}

	// Parse spec for version
	parts := strings.Split(spec, "@")
	if len(parts) != 2 {
		return fmt.Errorf("invalid tool spec for %s: %s", toolName, spec)
	}
	tag := parts[1]

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	globalToolDir := filepath.Join(home, ".chainlink", "tools", toolName, tag)

	// Find the actual binary
	var binaryPath string
	err = filepath.Walk(globalToolDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == "" {
			binaryPath = path
			return filepath.SkipDir
		}
		return nil
	})

	if err != nil || binaryPath == "" {
		return fmt.Errorf("could not find binary for tool %s in %s", toolName, globalToolDir)
	}

	// Execute the binary with passed arguments
	args := os.Args[1:]
	cmd := exec.Command(binaryPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	return cmd.Run()
}
