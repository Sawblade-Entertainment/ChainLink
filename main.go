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
	Package  PackageInfo       `toml:"Package"`
	Tools    map[string]string `toml:"Tools"`
	Packages map[string]string `toml:"Packages"`
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
		} else {
			// If chain.toml is missing, we can't resolve the tool name
			fmt.Printf("Tool %s not defined in chain.toml (config missing)\n", invokedAs)
			os.Exit(1)
		}

		if err := proxyToTool(toolName); err != nil {
			// Only print the error if the tool was actually not found.
			// If the tool ran but returned an exit code, proxyToTool already handled the execution.
			fmt.Printf("Tool %s not defined in chain.toml\n", invokedAs)
			os.Exit(1)
		}
		return
	}

	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	cmd := os.Args[1]
	switch cmd {
	case "help":
		printUsage()
	case "add":
		if len(os.Args) < 3 {
			fmt.Println("Usage: chainlink add <spec> [--tool]")
			os.Exit(1)
		}
		if err := handleAdd(os.Args[2:]); err != nil {
			fmt.Printf("Error adding: %v\n", err)
			os.Exit(1)
		}
	case "remove":
		if len(os.Args) < 3 {
			fmt.Println("Usage: chainlink remove <name>")
			os.Exit(1)
		}
		if err := handleRemove(os.Args[2]); err != nil {
			fmt.Printf("Error removing: %v\n", err)
			os.Exit(1)
		}
	case "install":
		if len(os.Args) > 2 {
			fmt.Println("Error: 'chainlink install' does not take arguments.")
			os.Exit(1)
		}
		if err := installAll(); err != nil {
			fmt.Printf("Error installing: %v\n", err)
			os.Exit(1)
		}
	case "plugin":
		if err := installPlugin(); err != nil {
			fmt.Printf("Error installing plugin: %v\n", err)
			os.Exit(1)
		}
	case "serve":

		if err := serve(); err != nil {
			fmt.Printf("Error serving: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Printf("Unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println("Usage: chainlink <command> [args]")
	fmt.Println("Commands: install, plugin, add, remove, serve, help")
}

func handleRemove(name string) error {
	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		return fmt.Errorf("could not find chain.toml: %w", err)
	}

	// Check tools first (case-insensitive)
	found := false
	for k := range config.Tools {
		if strings.EqualFold(k, name) {
			delete(config.Tools, k)
			found = true
			break
		}
	}

	// Check packages (case-insensitive)
	if !found {
		for k := range config.Packages {
			if strings.EqualFold(k, name) {
				delete(config.Packages, k)
				found = true
				break
			}
		}
	}

	if !found {
		return fmt.Errorf("item %s not found in tools or packages", name)
	}

	return writeConfig(&config)
}

func handleAdd(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("no specification provided")
	}

	spec := args[0]
	isTool := false
	isPackage := false
	for _, arg := range args[1:] {
		if arg == "--tool" {
			isTool = true
		} else if arg == "--package" {
			isPackage = true
		}
	}

	if isTool && isPackage {
		return fmt.Errorf("cannot specify both --tool and --package")
	}

	// Heuristic: assume tool if it contains capital letters, otherwise package
	if !isTool && !isPackage {
		isTool = containsUppercase(spec)
	}

	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		// Create new config if not exists
		config.Tools = make(map[string]string)
		config.Packages = make(map[string]string)
	}

	if isTool {
		// Tool logic: derive name from owner/repo@tag
		parts := strings.Split(spec, "@")
		if len(parts) < 2 {
			return fmt.Errorf("invalid tool spec. Expected format 'owner/repo@tag'")
		}
		repo := parts[0]
		repoParts := strings.Split(repo, "/")
		name := repoParts[len(repoParts)-1]
		config.Tools[name] = spec
	} else {
		// Package logic: derive name from scope/name@version
		parts := strings.Split(spec, "@")
		if len(parts) < 2 {
			return fmt.Errorf("invalid package spec. Expected format 'scope/name@version'")
		}
		scopeName := parts[0]
		scopeParts := strings.Split(scopeName, "/")
		name := scopeParts[len(scopeParts)-1]
		config.Packages[name] = spec
	}

	return writeConfig(&config)
}

func containsUppercase(s string) bool {
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'À' && r <= 'Þ') || (r >= 'ŕ' && r <= 'ž') {
			return true
		}
	}
	return false
}

func installAll() error {
	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		return fmt.Errorf("could not find chain.toml: %w", err)
	}

	fmt.Println("Installing packages...")
	if err := InstallPackageList(config.Packages); err != nil {
		return fmt.Errorf("package installation failed: %w", err)
	}

	fmt.Println("Installing tools...")
	if err := InstallToolList(config.Tools); err != nil {
		return fmt.Errorf("tool installation failed: %w", err)
	}

	return nil
}

// Remove the following functions entirely

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

	// Case-insensitive lookup for the spec
	var spec string
	var found bool
	for name, s := range config.Tools {
		if strings.EqualFold(name, toolName) {
			spec = s
			found = true
			break
		}
	}

	if !found {
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

	err = cmd.Run()
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			os.Exit(exitError.ExitCode())
		}
		return err
	}

	return nil
}
