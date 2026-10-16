package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed plugin/ChainLink.plugin.luau
var pluginSource string

const rbxmxTemplate = `<?xml version="1.0" encoding="utf-8"?>
<roblox xmlns:xmime="http://www.w3.org/2005/01/xmlmime" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:noNamespaceSchemaLocation="http://www.roblox.com/roblox.xsd" version="4">
	<Item class="Plugin" referent="RBX0">
		<Properties>
			<string name="Name">ChainLink</string>
		</Properties>
		<Item class="Script" referent="RBX1">
			<Properties>
				<string name="Name">ChainLink</string>
				<string name="Source">%s</string>
			</Properties>
		</Item>
	</Item>
</roblox>`

func getRobloxPluginsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	switch runtime.GOOS {
	case "windows":
		return filepath.Join(home, "AppData", "Local", "Roblox", "Plugins"), nil
	case "darwin":
		return filepath.Join(home, "Documents", "Roblox", "Plugins"), nil
	case "linux":
		return filepath.Join(home, ".local", "share", "roblox", "Plugins"), nil
	default:
		return "", fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func installPlugin() error {
	rbxmxContent := fmt.Sprintf(rbxmxTemplate, pluginSource)

	pluginsDir, err := getRobloxPluginsDir()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(pluginsDir, 0755); err != nil {
		return fmt.Errorf("failed to create plugins directory: %w", err)
	}

	destPath := filepath.Join(pluginsDir, "ChainLink.rbxmx")
	err = os.WriteFile(destPath, []byte(rbxmxContent), 0644)
	if err != nil {
		return fmt.Errorf("failed to write rbxmx file: %w", err)
	}

	fmt.Printf("Successfully installed ChainLink plugin to %s\n", destPath)
	return nil
}
