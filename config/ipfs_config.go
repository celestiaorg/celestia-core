package config

// IPFSConfig defines a subset of the IPFS config that will be passed to the IPFS init and IPFS node (as a service)
// spun up by the tendermint node.
// It is mostly concerned about port configuration (Addresses).
type IPFSConfig struct {
	// is where the generated IPFS config and files will be stored.
	// The default is ~/.tendermint/ipfs.
	ConfigRootPath string
	// TODO: can we avoid copying the fields from ipfs' config.Addresses here?
	// TODO: also, these are only used on init. Maybe ConfigRootPath is sufficient?
	API     string // address for the local API (RPC)
	Gateway string // address to listen on for IPFS HTTP object gateway
	// swarm related options:
	Swarm      []string // addresses for the swarm to listen on
	Announce   []string // swarm addresses to announce to the network
	NoAnnounce []string // swarm addresses not to announce to the network
}

// DefaultIPFSConfig returns a default config different from the default IPFS config.
// This avoids conflicts with existing installations when running LazyLedger-core node
// locally for testing purposes.
func DefaultIPFSConfig() *IPFSConfig {
	return &IPFSConfig{
		ConfigRootPath: ".ipfs/",
		API:            "/ip4/127.0.0.1/tcp/5002",
		Gateway:        "/ip4/127.0.0.1/tcp/5002",
		Swarm:          []string{"/ip4/0.0.0.0/tcp/4002", "/ip6/::/tcp/4002"},
	}
}

func (cfg *Config) IPFSRepoRoot() string {
	return rootify(cfg.IPFS.ConfigRootPath, cfg.RootDir)
}
