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
		// Try case-insensitive match for the tool name
		toolName := invokedAs

		// We need the config to check for case-insensitive matches
		var config ChainLinkConfig
		if _, err := toml.DecodeFile("chain.toml", &config); err == nil {
			for name := range config.Tools {
				if strings.EqualFold(name, invokedAs) {
					toolName = name
					break
				}
			}
		}

		if err := proxyToTool(toolName); err != nil {
			fmt.Printf("Tool %s not installed in current directory\n", invokedAs)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) < 2 {
		fmt.Println("Usage: chainlink <command> [args]")
		fmt.Println("Commands: install, tool, pack")
		os.Exit(0)
	}

	cmd := os.Args[1]
	switch cmd {
	case "install":
		if len(os.Args) > 2 {
			fmt.Println("Error: 'chainlink install' does not take arguments.")
			os.Exit(1)
		}
		if err := installAll(); err != nil {
			fmt.Printf("Error installing: %v\n", err)
			os.Exit(1)
		}
	case "tool":
		handleToolCommand()
	case "pack":
		handlePackCommand()
	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		fmt.Println("Usage: chainlink <command> [args]")
		fmt.Println("Commands: install, tool, pack")
		os.Exit(1)
	}
}

func installAll() error {
	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		return fmt.Errorf("could not find chain.toml: %w", err)
	}

	fmt.Println("Installing packages...")
	if err := installPackagesOnly(); err != nil {
		return fmt.Errorf("package installation failed: %w", err)
	}

	fmt.Println("Installing tools...")
	if err := installToolsOnly(); err != nil {
		return fmt.Errorf("tool installation failed: %w", err)
	}

	return nil
}

func handleToolCommand() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: chainlink tool <add|remove|list|install> [name@version]")
		os.Exit(1)
	}

	subCmd := os.Args[2]
	switch subCmd {
	case "add":
		if len(os.Args) < 4 {
			fmt.Println("Usage: chainlink tool add <name@spec>")
			os.Exit(1)
		}
		if err := addTool(os.Args[3]); err != nil {
			fmt.Printf("Error adding tool: %v\n", err)
			os.Exit(1)
		}
	case "remove":
		if len(os.Args) < 4 {
			fmt.Println("Usage: chainlink tool remove <name>")
			os.Exit(1)
		}
		if err := removeTool(os.Args[3]); err != nil {
			fmt.Printf("Error removing tool: %v\n", err)
			os.Exit(1)
		}
	case "list":
		listTools()
	case "install":
		if err := installToolsOnly(); err != nil {
			fmt.Printf("Error installing tools: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Unknown tool command: %s\n", subCmd)
		fmt.Println("Usage: chainlink tool <add|remove|list|install> [name@version]")
		os.Exit(1)
	}
}

func handlePackCommand() {
	if len(os.Args) < 3 {
		fmt.Println("Usage: chainlink pack <add|remove|list|install> [name@version]")
		os.Exit(1)
	}

	subCmd := os.Args[2]
	switch subCmd {
	case "add":
		if len(os.Args) < 4 {
			fmt.Println("Usage: chainlink pack add <name@spec>")
			os.Exit(1)
		}
		if err := addPackage(os.Args[3]); err != nil {
			fmt.Printf("Error adding package: %v\n", err)
			os.Exit(1)
		}
	case "remove":
		if len(os.Args) < 4 {
			fmt.Println("Usage: chainlink pack remove <name>")
			os.Exit(1)
		}
		if err := removePackage(os.Args[3]); err != nil {
			fmt.Printf("Error removing package: %v\n", err)
			os.Exit(1)
		}
	case "list":
		listPackages()
	case "install":
		if err := installPackagesOnly(); err != nil {
			fmt.Printf("Error installing packages: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Unknown pack command: %s\n", subCmd)
		fmt.Println("Usage: chainlink pack <add|remove|list|install> [name@version]")
		os.Exit(1)
	}
}

func addTool(spec string) error {
	// Expects name@owner/repo@tag or just owner/repo@tag
	// The current system uses the tool name as the key in chain.toml
	parts := strings.Split(spec, "@")
	if len(parts) < 2 {
		return fmt.Errorf("invalid tool spec. Expected name@owner/repo@tag")
	}

	// The original logic in main.go used config.Tools[toolName] = spec
	// where spec was "owner/repo@tag".
	// We need to determine the "name" of the tool.
	// For now, let's assume format: name:owner/repo@tag
	var name, toolSpec string
	if strings.Contains(spec, ":") {
		sParts := strings.SplitN(spec, ":", 2)
		name = sParts[0]
		toolSpec = sParts[1]
	} else {
		// Fallback: use the repo name as the tool name
		repoParts := strings.Split(parts[0], "/")
		name = repoParts[len(repoParts)-1]
		toolSpec = spec
	}

	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		// Create new config if not exists
		config.Tools = make(map[string]string)
		config.Packages = make(map[string]string)
	}

	config.Tools[name] = toolSpec
	return writeConfig(&config)
}

func removeTool(name string) error {
	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		return fmt.Errorf("could not find chain.toml: %w", err)
	}
	delete(config.Tools, name)
	return writeConfig(&config)
}

func listTools() {
	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		fmt.Println("No chain.toml found.")
		return
	}
	fmt.Println("Installed Tools:")
	for name, spec := range config.Tools {
		fmt.Printf("- %s: %s\n", name, spec)
	}
}

func installToolsOnly() error {
	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		return fmt.Errorf("could not find chain.toml: %w", err)
	}
	if err := InstallToolList(config.Tools); err != nil {
		return err
	}
	return SetupProjectBin(config.Tools)
}

func addPackage(spec string) error {
	// Format: alias:scope/name@version
	var alias, pkgSpec string
	if strings.Contains(spec, ":") {
		sParts := strings.SplitN(spec, ":", 2)
		alias = sParts[0]
		pkgSpec = sParts[1]
	} else {
		// Fallback: use scope/name as alias
		parts := strings.Split(spec, "@")
		alias = parts[0]
		pkgSpec = spec
	}

	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		config.Tools = make(map[string]string)
		config.Packages = make(map[string]string)
	}

	config.Packages[alias] = pkgSpec
	return writeConfig(&config)
}

func removePackage(alias string) error {
	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		return fmt.Errorf("could not find chain.toml: %w", err)
	}
	delete(config.Packages, alias)
	return writeConfig(&config)
}

func listPackages() {
	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		fmt.Println("No chain.toml found.")
		return
	}
	fmt.Println("Installed Packages:")
	for alias, spec := range config.Packages {
		fmt.Printf("- %s: %s\n", alias, spec)
	}
}

func installPackagesOnly() error {
	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		return fmt.Errorf("could not find chain.toml: %w", err)
	}
	return InstallPackageList(config.Packages)
}

func writeConfig(config *ChainLinkConfig) error {
	f, err := os.Create("chain.toml")
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(config)
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
	if !strings.HasPrefix(tag, "v") {
		vTag := "v" + tag
		vGlobalToolDir := filepath.Join(home, ".chainlink", "tools", toolName, vTag)
		if _, err := os.Stat(vGlobalToolDir); err == nil {
			globalToolDir = vGlobalToolDir
		}
	}

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
