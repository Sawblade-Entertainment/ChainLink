package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

type ServeConfig struct {
	ClientDir string
	ServerDir string
	SharedDir string
	PrefabDir string
}

// serve handles the "chainlink serve" command.
func serve() error {
	fmt.Println("Starting ChainLink serve...")

	// Default directory structure as requested
	config := ServeConfig{
		ClientDir: "client",
		ServerDir: "server",
		SharedDir: "shared",
		PrefabDir: "prefab",
	}

	// Verify directories exist or notify user
	dirs := []string{config.ClientDir, config.ServerDir, config.SharedDir, config.PrefabDir}
	for _, d := range dirs {
		if _, err := os.Stat(d); os.IsNotExist(err) {
			fmt.Printf("Warning: Directory %s not found. It will be ignored until created.\n", d)
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create watcher: %w", err)
	}
	defer watcher.Close()

	done := make(chan bool)
	go func() {
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&fsnotify.Write == fsnotify.Write {
					if isSyncableFile(event.Name) {
						fmt.Printf("File changed: %s -> Syncing to Roblox...\n", event.Name)
						// In a real implementation, this is where the Roblox sync logic goes.
						// For now, we simulate the sync.
						if err := syncToRoblox(event.Name, config); err != nil {
							log.Printf("Error syncing %s: %v", event.Name, err)
						}
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("Watcher error: %v", err)
			}
		}
	}()

	// Add directories and their subdirectories to the watcher
	for _, d := range dirs {
		if err := watchDirRecursive(watcher, d); err != nil {
			log.Printf("Error watching %s: %v", d, err)
		}
	}

	fmt.Println("Listening for changes in client/, server/, and shared/...")
	fmt.Println("Press Ctrl+C to stop.")
	<-done
	return nil
}

func watchDirRecursive(watcher *fsnotify.Watcher, path string) error {
	return filepath.Walk(path, func(walkPath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return watcher.Add(walkPath)
		}
		return nil
	})
}

func isSyncableFile(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".luau", ".lua", ".json", ".txt", ".rbxm", ".rbxmx":
		return true
	}
	return false
}

func syncToRoblox(path string, config ServeConfig) error {
	// Determine destination based on directory
	var destination string

	// Use relative path to check which directory it belongs to
	if strings.HasPrefix(path, config.ClientDir+string(os.PathSeparator)) || path == config.ClientDir {
		destination = "StarterPlayer.StarterPlayerScripts"
	} else if strings.HasPrefix(path, config.ServerDir+string(os.PathSeparator)) || path == config.ServerDir {
		destination = "ServerScriptService"
	} else if strings.HasPrefix(path, config.SharedDir+string(os.PathSeparator)) || path == config.SharedDir {
		destination = "ReplicatedStorage"
	} else if strings.HasPrefix(path, config.PrefabDir+string(os.PathSeparator)) || path == config.PrefabDir {
		destination = "ServerStorage"
	} else {
		return fmt.Errorf("file %s is not in a syncable directory", path)
	}

	// Simulation: in a real version, this would use a Roblox API or a local proxy.
	fmt.Printf("[SYNC] %s -> Roblox:%s\n", filepath.Base(path), destination)
	return nil
}
