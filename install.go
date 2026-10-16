package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// InstallChainlink sets up the Chainlink environment.
// It creates ~/.chainlink/bin and provides instructions to add it to the PATH.
func InstallChainlink() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	binDir := filepath.Join(home, ".chainlink", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("failed to create bin directory: %w", err)
	}

	fmt.Println("Chainlink environment initialized.")
	fmt.Printf("Bin directory created at: %s\n", binDir)

	var shellConfig string
	switch runtime.GOOS {
	case "darwin", "linux":
		shell := os.Getenv("SHELL")
		if strings.Contains(shell, "zsh") {
			shellConfig = filepath.Join(home, ".zshrc")
		} else {
			shellConfig = filepath.Join(home, ".bashrc")
		}
	case "windows":
		fmt.Println("\nTo add Chainlink to your PATH on Windows:")
		fmt.Printf("1. Search for 'Edit the system environment variables'\n")
		fmt.Printf("2. Click 'Environment Variables'\n")
		fmt.Printf("3. Under 'User variables', find PATH and click 'Edit'\n")
		fmt.Printf("4. Add a new entry: %s\n", binDir)
		return nil
	default:
		return fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}

	pathLine := fmt.Sprintf("\nexport PATH=\"%s:$PATH\"\n", binDir)

	// Check if already in PATH
	currentPath := os.Getenv("PATH")
	if strings.Contains(currentPath, binDir) {
		fmt.Printf("\n%s is already in your PATH. Skipping PATH update.\n", binDir)
		return nil
	}

	f, err := os.OpenFile(shellConfig, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("failed to open shell config %s: %w", shellConfig, err)
	}
	defer f.Close()

	if _, err := f.WriteString(pathLine); err != nil {
		return fmt.Errorf("failed to write to shell config: %w", err)
	}

	fmt.Printf("\nAdded %s to %s\n", binDir, shellConfig)
	fmt.Println("Please restart your terminal or run: source " + shellConfig)

	return nil
}
