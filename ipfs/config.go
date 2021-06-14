package ipfs

import "path/filepath"

// Config defines a subset of the IPFS config that will be passed to the IPFS init and IPFS node (as a service)
// spun up by the tendermint node.
// It is mostly concerned about port configuration (Addresses).
type Config struct {
	RootDir string
	// is where the generated IPFS config and files will be stored.
	// The default is ~/.tendermint/ipfs.
	RepoPath string `mapstructure:"repo-path"`
	ServeAPI bool   `mapstructure:"serve-api"`
}

// DefaultConfig returns a default config different from the default IPFS config.
// This avoids conflicts with existing installations when running LazyLedger-core node
// locally for testing purposes.
func DefaultConfig() *Config {
	return &Config{
		RepoPath: "ipfs",
		ServeAPI: false,
	}
}

func (cfg *Config) Path() string {
	if filepath.IsAbs(cfg.RepoPath) {
		return cfg.RepoPath
	}
	return filepath.Join(cfg.RootDir, cfg.RepoPath)
}
