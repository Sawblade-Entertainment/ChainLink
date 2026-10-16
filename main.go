package main

import (
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

	toml.DecodeFile("chain.toml", &config)

	InstallPackageList(config.Packages)
}
