package main

import (
	"fmt"
	"os"

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
