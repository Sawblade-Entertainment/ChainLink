package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ulikunitz/xz"
)

type GitHubRelease struct {
	Assets []GitHubAsset `json:"assets"`
}

type GitHubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// InstallToolList installs multiple tools from a map.
func InstallToolList(tools map[string]string) error {
	for name, spec := range tools {
		fmt.Printf("Installing tool %s: %s\n", name, spec)
		if err := InstallTool(spec); err != nil {
			return fmt.Errorf("failed to install tool %s (%s): %w", name, spec, err)
		}
	}
	return nil
}

// InstallTool downloads a tool release from GitHub by tag.
// Format: "owner/repo@tag" (e.g., "Kampfkarren/selene@0.32.0")
func InstallTool(toolSpec string) error {
	parts := strings.Split(toolSpec, "@")
	if len(parts) != 2 {
		return fmt.Errorf("invalid tool format: %s. Expected format 'owner/repo@tag'", toolSpec)
	}

	repo := parts[0] // "Kampfkarren/selene"
	tag := parts[1]  // "0.32.0"

	// GitHub tags often start with 'v', but users might omit it.
	// We try the tag as-is first, and if that fails with a 404, we try adding 'v'.
	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repo, tag)

	resp, err := http.Get(apiURL)
	if err != nil {
		return fmt.Errorf("failed to fetch release info: %w", err)
	}

	if resp.StatusCode == http.StatusNotFound && !strings.HasPrefix(tag, "v") {
		resp.Body.Close()
		tag = "v" + tag
		apiURL = fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repo, tag)
		resp, err = http.Get(apiURL)
		if err != nil {
			return fmt.Errorf("failed to fetch release info with 'v' prefix: %w", err)
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("github API returned status: %s", resp.Status)
	}

	var release GitHubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("failed to decode release JSON: %w", err)
	}

	if len(release.Assets) == 0 {
		return fmt.Errorf("no assets found for release %s", tag)
	}

	// Filter assets by GOOS and GOARCH
	var targetAsset *GitHubAsset
	for _, asset := range release.Assets {
		name := strings.ToLower(asset.Name)

		// Map runtime.GOOS and runtime.GOARCH to common GitHub release naming patterns
		osMatch := strings.Contains(name, runtime.GOOS) ||
			(runtime.GOOS == "darwin" && strings.Contains(name, "macos")) ||
			(runtime.GOOS == "windows" && strings.Contains(name, "windows")) ||
			(runtime.GOOS == "linux" && strings.Contains(name, "linux"))

		archMatch := strings.Contains(name, runtime.GOARCH) ||
			(runtime.GOARCH == "arm64" && strings.Contains(name, "aarch64")) ||
			(runtime.GOARCH == "amd64" && strings.Contains(name, "x86_64"))

		// Some tools (like Selene) provide generic "macos" or "linux" assets that work across archs
		// or they might not specify the arch in the filename if there's only one binary.
		// If we find a match for OS but not Arch, we'll keep it as a fallback.
		if osMatch && archMatch {
			targetAsset = &asset
			break
		}
	}

	// Fallback: if no OS+Arch match, try just OS match
	if targetAsset == nil {
		for _, asset := range release.Assets {
			name := strings.ToLower(asset.Name)
			if strings.Contains(name, runtime.GOOS) ||
				(runtime.GOOS == "darwin" && strings.Contains(name, "macos")) ||
				(runtime.GOOS == "windows" && strings.Contains(name, "windows")) ||
				(runtime.GOOS == "linux" && strings.Contains(name, "linux")) {
				targetAsset = &asset
				break
			}
		}
	}

	if targetAsset == nil {
		return fmt.Errorf("no suitable asset found for %s/%s on %s/%s", repo, tag, runtime.GOOS, runtime.GOARCH)
	}

	asset := *targetAsset

	binDir := "./bin"
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("failed to create bin directory: %w", err)
	}

	destPath := filepath.Join(binDir, asset.Name)
	fmt.Printf("Downloading %s to %s...\n", asset.Name, destPath)

	if err := downloadFile(asset.BrowserDownloadURL, destPath); err != nil {
		return fmt.Errorf("failed to download asset: %w", err)
	}

	// Unzip if the file is a zip archive
	if strings.HasSuffix(strings.ToLower(asset.Name), ".zip") {
		fmt.Printf("Unzipping %s...\n", asset.Name)
		if err := unzipTool(destPath, binDir); err != nil {
			return fmt.Errorf("failed to unzip %s: %w", asset.Name, err)
		}
		os.Remove(destPath) // Clean up the zip file
	} else if strings.HasSuffix(strings.ToLower(asset.Name), ".tar.gz") || strings.HasSuffix(strings.ToLower(asset.Name), ".tgz") {
		fmt.Printf("Untarring %s...\n", asset.Name)
		if err := untarTool(destPath, binDir); err != nil {
			return fmt.Errorf("failed to untar %s: %w", asset.Name, err)
		}
		os.Remove(destPath) // Clean up the tar file
	} else if strings.HasSuffix(strings.ToLower(asset.Name), ".tar.xz") {
		fmt.Printf("Untarring (xz) %s...\n", asset.Name)
		if err := untarXZTool(destPath, binDir); err != nil {
			return fmt.Errorf("failed to untar xz %s: %w", asset.Name, err)
		}
		os.Remove(destPath) // Clean up the tar file
	}

	// Make all files in bin directory executable if they look like binaries
	// In a simple tool, we just try to chmod everything in the bin dir
	// that doesn't have an extension, or we can just chmod the extracted files.
	if err := makeBinariesExecutable(binDir); err != nil {
		return fmt.Errorf("failed to make binaries executable: %w", err)
	}

	return nil
}

func untarXZTool(src, dest string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()

	xzr, err := xz.NewReader(file)
	if err != nil {
		return err
	}

	tr := tar.NewReader(xzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}

			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}

			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

func untarTool(src, dest string) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer file.Close()

	gzr, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		target := filepath.Join(dest, header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return err
			}

			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(header.Mode))
			if err != nil {
				return err
			}

			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
	return nil
}

func unzipTool(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		fpath := filepath.Join(dest, f.Name)

		if f.FileInfo().IsDir() {
			os.MkdirAll(fpath, os.ModePerm)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(fpath), os.ModePerm); err != nil {
			return err
		}

		outFile, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		_, err = io.Copy(outFile, rc)

		outFile.Close()
		rc.Close()

		if err != nil {
			return err
		}
	}
	return nil
}

func makeBinariesExecutable(binDir string) error {
	return filepath.Walk(binDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == "" {
			return os.Chmod(path, 0755)
		}
		return nil
	})
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}
