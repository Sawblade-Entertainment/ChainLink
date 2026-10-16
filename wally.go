package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const wallyRegistryAPI = "https://api.wally.run/v1/package-contents"

// InstallPackageList installs a list of wally packages from a map where keys are aliases and values are package names (e.g., "vibeonline/razorinput@0.1.0")
func InstallPackageList(packages map[string]string) error {
	for alias, packageName := range packages {
		fmt.Printf("Installing %s...\n", packageName)
		if err := InstallPackage(alias, packageName); err != nil {
			return fmt.Errorf("failed to install package %s: %w", packageName, err)
		}
	}
	return nil
}

// InstallPackage installs a wally package by its name (e.g., "vibeonline/razorinput@0.1.0")
// It downloads the package zip directly from the Wally registry and extracts it.
func InstallPackage(alias, packageName string) error {
	// Parse package name: "scope/name@version"
	parts := strings.Split(packageName, "@")
	if len(parts) != 2 {
		return fmt.Errorf("invalid package name format: %s. Expected scope/name@version", packageName)
	}

	scopeName := parts[0]
	version := parts[1]

	scopeParts := strings.Split(scopeName, "/")
	if len(scopeParts) != 2 {
		return fmt.Errorf("invalid scope/name format: %s. Expected scope/name", scopeName)
	}

	scope := scopeParts[0]
	name := scopeParts[1]

	// Construct the registry URL
	url := fmt.Sprintf("%s/%s/%s/%s", wallyRegistryAPI, scope, name, version)

	fmt.Printf("Downloading package %s from %s...\n", packageName, url)

	client := &http.Client{
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
		},
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %v", err)
	}

	// Use the official Wally CLI User-Agent to avoid 426 Upgrade Required errors
	req.Header.Set("User-Agent", "wally/0.3.2")
	req.Header.Set("Wally-Version", "0.3.2")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to request package: %v", err)
	}
	defer resp.Body.Close()

	fmt.Printf("Response status: %s\n", resp.Status)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry returned non-OK status: %s", resp.Status)
	}

	// Create a temporary file to hold the zip
	tmpFile, err := os.CreateTemp("", "wally-package-*.zip")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %v", err)
	}
	tmpFileName := tmpFile.Name()
	defer os.Remove(tmpFileName)
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		return fmt.Errorf("failed to save package to temp file: %v", err)
	}

	// Ensure data is flushed to disk before reading back via zip.OpenReader
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %v", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %v", err)
	}

	// Ensure the base packages directory exists
	if err := os.MkdirAll("packages", os.ModePerm); err != nil {
		return fmt.Errorf("failed to create packages directory: %v", err)
	}

	// Unzip the package into owner@name folder
	if err := unzip(tmpFileName, "packages/"+alias); err != nil {
		return fmt.Errorf("failed to extract package: %v", err)
	}

	fmt.Printf("Successfully installed %s to packages\n", packageName)
	return nil
}

func unzip(src string, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)

		// Check for ZipSlip (directory traversal)
		if !strings.HasPrefix(fpath, filepath.Clean(dest)+string(os.PathSeparator)) &&
			fpath != filepath.Clean(dest) {
			return fmt.Errorf("illegal file path in zip: %s", fpath)
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}

		out, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}

		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}
	return nil
}
