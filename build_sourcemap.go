package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type SourcemapNode struct {
	Name      string           `json:"name"`
	ClassName string           `json:"className"`
	FilePaths []string         `json:"filePaths,omitempty"`
	Children  []*SourcemapNode `json:"children,omitempty"`
}

func handleSourcemap(outputPath string) error {
	cfg := loadConfig()
	root, err := os.Getwd()
	if err != nil {
		return err
	}

	b := NewBuilder(cfg, root)

	// Mimic the build process to populate the tree
	for _, s := range []string{"Workspace", "ReplicatedStorage", "ServerScriptService", "ServerStorage", "StarterPlayer"} {
		b.service(s)
	}
	b.resolveTarget("StarterPlayer.StarterPlayerScripts")

	b.syncBootstrap()

	for _, r := range cfg.Roots {
		absDir := filepath.Join(root, r.Dir)
		if dirExists(absDir) {
			b.syncDir(absDir, r.Target, "")
		}
	}
	for _, p := range cfg.Packages {
		absDir := filepath.Join(packagesBase(), filepath.FromSlash(p.PackageName))
		if !dirExists(absDir) {
			continue
		}

		srcDir := filepath.Join(absDir, "src")
		if dirExists(srcDir) {
			b.syncDir(srcDir, p.Target, "")
		} else {
			b.syncDir(absDir, p.Target, "")
		}
	}

	// The root of the sourcemap is a virtual node representing the project.
	// Using "Teast" as name and "DataModel" as class based on user example.
	rootNode := &SourcemapNode{
		Name:      "Teast",
		ClassName: "DataModel",
		FilePaths: []string{"chain.toml"},
	}

	for _, inst := range b.top {
		rootNode.Children = append(rootNode.Children, convertToSourcemapNode(inst))
	}

	data, err := json.MarshalIndent(rootNode, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal sourcemap: %w", err)
	}

	if outputPath != "" {
		if err := os.WriteFile(outputPath, data, 0644); err != nil {
			return fmt.Errorf("failed to write sourcemap to file: %w", err)
		}
	} else {
		fmt.Println(string(data))
	}
	return nil
}

func convertToSourcemapNode(inst *Instance) *SourcemapNode {
	node := &SourcemapNode{
		Name:      inst.Name,
		ClassName: inst.Class,
	}

	if inst.FilePath != "" {
		node.FilePaths = []string{inst.FilePath}
	}

	for _, child := range inst.Children {
		node.Children = append(node.Children, convertToSourcemapNode(child))
	}

	return node
}
