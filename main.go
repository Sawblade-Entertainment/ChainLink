package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"golang.org/x/term"
)

type PackageInfo struct {
	Name        string
	Version     string
	License     string
	Description string
	Website     string
}

type ChainLinkConfig struct {
	Package  PackageInfo       `toml:"Package"`
	Tools    map[string]string `toml:"Tools"`
	Packages map[string]string `toml:"Packages"`
}

var (
	ExampleController = `function mod:init()
	print("Hello world!")
end`
	ExampleService = `function mod:init()
	print("Hello world!")
end`
	ExampleData = `{
	"hello": "world"
}`
	VSCodeSettings = `{
    "luau-lsp.sourcemap.rojoPath": "chainlink",
    "luau-lsp.sourcemap.rojoProjectFile": "chain.toml",
	"luau-lsp.sourcemap.useVSCodeWatcher": true,
    "luau-lsp.types.definitionFiles": [
        ".vscode/globals.d.luau"
    ]
}`
	VSCodeGlobals = `declare mod: any
declare require: (module: any) -> any
declare module: any`
)

func main() {
	// If we are called as a symlink for a tool, the first argument (os.Args[0])
	// will be the tool name.
	invokedAs := filepath.Base(os.Args[0])

	// If the binary name is NOT "chainlink", it means we were called via a symlink (e.g., "selene")
	if invokedAs != "chainlink" {
		// If the binary name starts with "chainlink-" (e.g., "chainlink-darwin-arm64"),
		// it's likely a release binary being run for the first time.
		// Default to global installation.
		if strings.HasPrefix(invokedAs, "chainlink-") {
			if err := handleGlobal(); err != nil {
				fmt.Printf("Error installing globally: %v\n", err)
				os.Exit(1)
			}
			return
		}

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
	case "init":
		if err := handleInit(); err != nil {
			fmt.Printf("Error initializing: %v\n", err)
			os.Exit(1)
		}
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
	case "login":
		if err := handleLogin(os.Args[2:]); err != nil {
			fmt.Printf("Error logging in: %v\n", err)
			os.Exit(1)
		}
	case "publish":
		if err := handlePublish(os.Args[2:]); err != nil {
			fmt.Printf("Error publishing: %v\n", err)
			os.Exit(1)
		}
	case "serve":
		if err := serve(); err != nil {
			fmt.Printf("Error serving: %v\n", err)
			os.Exit(1)
		}
	case "build":
		if len(os.Args) < 3 {
			fmt.Println("Usage: chainlink build <output.rbxlx>")
			os.Exit(1)
		}
		if err := handleBuild(os.Args[2]); err != nil {
			fmt.Printf("Error building: %v\n", err)
			os.Exit(1)
		}
	case "sourcemap":
		outputPath := ""
		for i := 2; i < len(os.Args); i++ {
			if os.Args[i] == "-o" && i+1 < len(os.Args) {
				outputPath = os.Args[i+1]
				break
			}
		}
		if err := handleSourcemap(outputPath); err != nil {
			fmt.Printf("Error generating sourcemap: %v\n", err)
			os.Exit(1)
		}
	case "run":

		if len(os.Args) < 3 {
			fmt.Println("Usage: chainlink run <spec> [args]")
			os.Exit(1)
		}
		spec := os.Args[2]
		if err := runToolBySpec(spec, os.Args[3:]); err != nil {
			fmt.Printf("Error running tool %s: %v\n", spec, err)
			os.Exit(1)
		}
	case "global":
		if err := handleGlobal(); err != nil {
			fmt.Printf("Error installing globally: %v\n", err)
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
	fmt.Println("Commands: init, install, plugin, login, publish, add, remove, serve, build, run, global, help")
}

func createPlaceholderFile(path string, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0644)
}

func handleInit() error {
	// 1. Git initialization
	if _, err := os.Stat(".git"); os.IsNotExist(err) {
		fmt.Println("Initializing git repository...")
		if err := exec.Command("git", "init").Run(); err != nil {
			return fmt.Errorf("failed to run git init: %w", err)
		}
	} else {
		fmt.Println("Git repository already exists.")
	}

	// 2. Create chain.toml
	if _, err := os.Stat("chain.toml"); err == nil {
		return fmt.Errorf("chain.toml already exists")
	}

	fmt.Println("Creating chain.toml...")
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current working directory: %w", err)
	}
	folderName := filepath.Base(cwd)

	config := ChainLinkConfig{
		Package: PackageInfo{
			Name:    folderName,
			Version: "0.1.0",
		},
		Tools:    make(map[string]string),
		Packages: make(map[string]string),
	}

	if err := writeConfig(&config); err != nil {
		return err
	}

	// 3. Create project structure
	fmt.Println("Creating project structure...")

	// Client: ExampleController
	if err := os.MkdirAll("client", 0755); err != nil {
		return fmt.Errorf("failed to create directory client: %w", err)
	}
	if err := createPlaceholderFile(filepath.Join("client", "ExampleController.luau"), ExampleController); err != nil {
		return err
	}

	// Server: ExampleService
	if err := os.MkdirAll("server", 0755); err != nil {
		return fmt.Errorf("failed to create directory server: %w", err)
	}
	if err := createPlaceholderFile(filepath.Join("server", "ExampleService.luau"), ExampleService); err != nil {
		return err
	}

	// Shared: ExampleData
	if err := os.MkdirAll("shared", 0755); err != nil {
		return fmt.Errorf("failed to create directory shared: %w", err)
	}
	if err := createPlaceholderFile(filepath.Join("shared", "ExampleData.json"), ExampleData); err != nil {
		return err
	}

	// VSCode configuration
	if err := createPlaceholderFile(filepath.Join(".vscode", "settings.json"), VSCodeSettings); err != nil {
		return err
	}
	if err := createPlaceholderFile(filepath.Join(".vscode", "globals.d.luau"), VSCodeGlobals); err != nil {
		return err
	}

	return nil
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

func writeConfig(config *ChainLinkConfig) error {
	f, err := os.Create("chain.toml")
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(config)
}

func handleGlobal() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("failed to get home directory: %w", err)
	}

	// We want to put it in ~/.chainlink/bin/chainlink
	binDir := filepath.Join(home, ".chainlink", "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		return fmt.Errorf("failed to create bin directory: %w", err)
	}

	destPath := filepath.Join(binDir, "chainlink")

	// Copy binary to global bin
	input, err := os.ReadFile(exePath)
	if err != nil {
		return fmt.Errorf("failed to read binary: %w", err)
	}
	if err := os.WriteFile(destPath, input, 0755); err != nil {
		return fmt.Errorf("failed to write binary to %s: %w", destPath, err)
	}

	fmt.Printf("Chainlink installed globally to %s\n", destPath)

	if err := InstallChainlink(); err != nil {
		fmt.Printf("Warning: failed to update PATH: %v\n", err)
	}

	return nil
}

type AuthStore struct {
	Tokens map[string]string `toml:"tokens"`
}

func authPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, ".chainlink", "auth.toml"), nil
}

func loadAuthStore() (*AuthStore, error) {
	path, err := authPath()
	if err != nil {
		return nil, err
	}
	store := &AuthStore{Tokens: map[string]string{}}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, store); err != nil {
		return nil, fmt.Errorf("malformed auth config at %s: %w", path, err)
	}
	if store.Tokens == nil {
		store.Tokens = map[string]string{}
	}
	return store, nil
}

func (s *AuthStore) save() error {
	path, err := authPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("failed to create auth directory: %w", err)
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(s); err != nil {
		return fmt.Errorf("failed to encode auth store: %w", err)
	}
	return os.WriteFile(path, buf.Bytes(), 0600)
}

func setToken(api, token string) error {
	key, err := normalizeURL(api)
	if err != nil {
		return err
	}
	store, err := loadAuthStore()
	if err != nil {
		return err
	}
	if token == "" {
		delete(store.Tokens, key)
	} else {
		store.Tokens[key] = token
	}
	return store.save()
}

func getToken(api string) (string, error) {
	key, err := normalizeURL(api)
	if err != nil {
		return "", err
	}
	store, err := loadAuthStore()
	if err != nil {
		return "", err
	}
	if t, ok := store.Tokens[key]; ok {
		return t, nil
	}
	if t, ok := store.Tokens[strings.TrimSuffix(key, "/")]; ok {
		return t, nil
	}
	return "", fmt.Errorf("no token found for %s", key)
}

func normalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("invalid URL %q", raw)
	}
	if u.Path == "" {
		u.Path = "/"
	}
	return u.String(), nil
}

type IndexConfig struct {
	API           string   `json:"api"`
	GithubOAuthID string   `json:"github_oauth_id,omitempty"`
	FallbackRegs  []string `json:"fallback_registries,omitempty"`
}

func fetchIndexConfig(registry string) (*IndexConfig, error) {
	u, err := url.Parse(strings.TrimSuffix(registry, ".git"))
	if err != nil {
		return nil, fmt.Errorf("invalid registry URL %q: %w", registry, err)
	}
	if u.Host != "github.com" {
		return nil, fmt.Errorf("only GitHub-hosted registries are supported (got %s); use --api", registry)
	}
	repoPath := strings.Trim(u.Path, "/")
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/HEAD/config.json", repoPath)

	resp, err := http.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch registry config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch registry config from %s: %s", rawURL, resp.Status)
	}

	var cfg IndexConfig
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("malformed registry config: %w", err)
	}
	if cfg.API == "" {
		return nil, fmt.Errorf("registry config has no 'api' field")
	}
	return &cfg, nil
}

func projectRegistry(projectPath string) string {
	var m struct {
		Package struct {
			Registry string `toml:"registry"`
		} `toml:"package"`
	}
	for _, name := range []string{"chain.toml", "wally.toml"} {
		if _, err := toml.DecodeFile(filepath.Join(projectPath, name), &m); err == nil && m.Package.Registry != "" {
			return m.Package.Registry
		}
	}
	return "https://github.com/UpliftGames/wally-index"
}

func handleLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	projectPath := fs.String("project-path", ".", "path to the project")
	api := fs.String("api", "", "API URL to store the token for")
	token := fs.String("token", "", "token to store (skips interactive login)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *token != "" && *api != "" {
		if err := setToken(*api, *token); err != nil {
			return err
		}
		fmt.Println("Token saved.")
		return nil
	}

	var cfg *IndexConfig
	if *api != "" {
		cfg = &IndexConfig{API: *api}
	} else {
		var err error
		cfg, err = fetchIndexConfig(projectRegistry(*projectPath))
		if err != nil {
			return err
		}
	}

	if *token != "" {
		if err := setToken(cfg.API, *token); err != nil {
			return err
		}
		fmt.Println("Token saved.")
		return nil
	}

	if cfg.GithubOAuthID == "" {
		return promptAPIKey(cfg.API)
	}
	return promptGithubAuth(cfg.API, cfg.GithubOAuthID)
}

func promptAPIKey(api string) error {
	fmt.Printf("Wally requires an API key to publish to %s.\n", api)
	fmt.Print("Enter an API key: ")

	var key string
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return fmt.Errorf("failed to read API key: %w", err)
		}
		key = string(b)
	} else {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("failed to read API key: %w", err)
		}
		key = line
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("API key cannot be empty")
	}
	if err := setToken(api, key); err != nil {
		return err
	}
	fmt.Println("API key saved.")
	return nil
}

type deviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

type accessTokenResponse struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
	Interval         int    `json:"interval"`
}

func postJSON(client *http.Client, endpoint string, body any, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, string(data))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func promptGithubAuth(api, clientID string) error {
	client := &http.Client{Timeout: 30 * time.Second}

	var dc deviceCodeResponse
	err := postJSON(client, "https://github.com/login/device/code", map[string]string{
		"client_id": clientID,
		"scope":     "read:user read:org",
	}, &dc)
	if err != nil {
		return fmt.Errorf("failed to start GitHub device flow: %w", err)
	}

	fmt.Println()
	fmt.Printf("Go to %s\n", dc.VerificationURI)
	fmt.Printf("And enter the code: %s\n", dc.UserCode)
	fmt.Println()
	fmt.Println("Awaiting authorization...")

	interval := time.Duration(dc.Interval) * time.Second
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(time.Duration(dc.ExpiresIn) * time.Second)

	for {
		time.Sleep(interval)
		if dc.ExpiresIn > 0 && time.Now().After(deadline) {
			return errors.New("device code expired, please try again")
		}

		var tr accessTokenResponse
		err := postJSON(client, "https://github.com/login/oauth/access_token", map[string]string{
			"client_id":   clientID,
			"device_code": dc.DeviceCode,
			"grant_type":  "urn:ietf:params:oauth:grant-type:device-code",
		}, &tr)
		if err != nil {
			return fmt.Errorf("failed to poll for access token: %w", err)
		}

		switch tr.Error {
		case "":
			if tr.AccessToken == "" {
				return errors.New("GitHub returned an empty access token")
			}
			if err := setToken(api, tr.AccessToken); err != nil {
				return err
			}
			fmt.Println("Authorization successful!")
			return nil
		case "authorization_pending":
			continue
		case "slow_down":
			if tr.Interval > 0 {
				interval = time.Duration(tr.Interval) * time.Second
			} else {
				interval += 5 * time.Second
			}
		case "expired_token":
			return errors.New("device code expired, please try again")
		case "access_denied":
			return errors.New("authorization was cancelled")
		default:
			return fmt.Errorf("GitHub auth error: %s (%s)", tr.Error, tr.ErrorDescription)
		}
	}
}

func handlePublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	tokenFlag := fs.String("token", "", "API token to use for publishing (overrides auth.toml)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var config ChainLinkConfig
	if _, err := toml.DecodeFile("chain.toml", &config); err != nil {
		return fmt.Errorf("could not find chain.toml: %w", err)
	}

	if config.Package.Name == "" || config.Package.Version == "" {
		return fmt.Errorf("package name and version must be defined in chain.toml for publishing")
	}

	fmt.Printf("Preparing to publish %s version %s...\n", config.Package.Name, config.Package.Version)

	tmpDir, err := os.MkdirTemp("", "chainlink-publish-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	if err := copyDir("src", filepath.Join(tmpDir, "src")); err != nil {
		fmt.Println("Warning: src directory not found, skipping.")
	}

	if err := copyDir("packages", filepath.Join(tmpDir, "packages")); err != nil {
		fmt.Println("Warning: packages directory not found, skipping.")
	}

	// Fetch the registry from the project's config to include it in the manifest
	registry := projectRegistry(".")

	wallyToml := fmt.Sprintf(`[package]
name = "%s"
version = "%s"
registry = "%s"
license = "%s"
description = "%s"
homepage = "%s"
realm = "shared"
`,
		config.Package.Name,
		config.Package.Version,
		registry,
		config.Package.License,
		config.Package.Description,
		config.Package.Website,
	)
	if err := os.WriteFile(filepath.Join(tmpDir, "wally.toml"), []byte(wallyToml), 0644); err != nil {
		return fmt.Errorf("failed to create wally.toml: %w", err)
	}

	zipPath := tmpDir + ".zip"
	if err := createZip(tmpDir, zipPath); err != nil {
		return fmt.Errorf("failed to create zip: %w", err)
	}
	defer os.Remove(zipPath)

	fmt.Println("Uploading to Wally registry...")

	zipData, err := os.ReadFile(zipPath)
	if err != nil {
		return fmt.Errorf("failed to read zip for upload: %w", err)
	}

	if len(zipData) > 2*1024*1024 {
		return fmt.Errorf("package size exceeds 2MB. Reduce package size and try again")
	}

	api := "https://api.wally.run"
	cfg, err := fetchIndexConfig(projectRegistry("."))
	if err == nil {
		api = cfg.API
	}

	var token string
	if *tokenFlag != "" {
		token = *tokenFlag
	} else {
		var err error
		token, err = getToken(api)
		if err != nil {
			return fmt.Errorf("authentication error: %w. Please run 'chainlink login' or use --token", err)
		}
	}

	publishURL := strings.TrimSuffix(api, "/") + "/v1/publish"

	req, err := http.NewRequest("POST", publishURL, bytes.NewReader(zipData))
	if err != nil {
		return fmt.Errorf("failed to create upload request: %w", err)
	}

	req.Header.Set("accept", "application/json")
	req.Header.Set("Wally-Version", "0.3.2")
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to execute upload request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		return fmt.Errorf("version already exists")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to publish package: %s\n%s", resp.Status, string(respBody))
	}

	fmt.Println("Package published successfully!")
	return nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, info.Mode())
	})
}

func createZip(source, target string) error {
	zipFile, err := os.Create(target)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	archive := zip.NewWriter(zipFile)
	defer archive.Close()

	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}

		f, err := archive.Create(rel)
		if err != nil {
			return err
		}

		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()

		_, err = io.Copy(f, srcFile)
		return err
	})
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

	return runToolBySpec(spec, os.Args[1:])
}

func runToolBySpec(spec string, args []string) error {
	parts := strings.Split(spec, "@")
	if len(parts) != 2 {
		return fmt.Errorf("invalid tool spec: %s", spec)
	}
	repo, tag := parts[0], parts[1]
	repoParts := strings.Split(repo, "/")
	toolName := repoParts[len(repoParts)-1]

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
		// Try to install the tool if it's missing
		fmt.Printf("Tool %s not found locally. Attempting to install...\n", toolName)
		if installErr := InstallTool(toolName, spec); installErr != nil {
			return fmt.Errorf("could not find binary for tool %s in %s and installation failed: %v", toolName, globalToolDir, installErr)
		}

		// Try to find the binary again after installation
		var postInstallBinaryPath string
		err = filepath.Walk(globalToolDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if !info.IsDir() && filepath.Ext(path) == "" {
				postInstallBinaryPath = path
				return filepath.SkipDir
			}
			return nil
		})

		if err != nil || postInstallBinaryPath == "" {
			return fmt.Errorf("installed tool %s but could not find binary in %s", toolName, globalToolDir)
		}
		binaryPath = postInstallBinaryPath
	}

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
