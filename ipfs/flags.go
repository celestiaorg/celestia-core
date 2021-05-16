package ipfs

import (
	"github.com/spf13/cobra"
)

// AddFlags sets IPFS related flags to the given command.
func AddFlags(cmd *cobra.Command, cfg *Config) {
	cmd.Flags().String(
		"ipfs.repo-path",
		cfg.RepoPath,
		"custom IPFS repository path. Defaults to `.{RootDir}/ipfs`",
	)
	cmd.Flags().Bool(
		"ipfs.serve-api",
		cfg.ServeAPI,
		"set this to expose IPFS API(useful for debugging)",
	)
	cmd.Flags().BoolVar(
		&EmbeddedInit,
		"ipfs.init",
		false,
		"set this to initialize repository for embedded IPFS node. Flag is ignored if repo is already initialized",
	)
}
