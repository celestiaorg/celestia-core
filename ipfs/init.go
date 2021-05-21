package ipfs

import (
	"github.com/ipfs/go-ipfs/repo/fsrepo"

	"github.com/lazyledger/lazyledger-core/libs/log"
	tmos "github.com/lazyledger/lazyledger-core/libs/os"
)

// InitRepo initialize IPFS repository under the given path.
// It does nothing if repo is already created.
func InitRepo(path string, logger log.Logger) error {
	if fsrepo.IsInitialized(path) {
		logger.Info("IPFS is already initialized", "ipfs-path", path)
		return nil
	}

	if err := plugins(path); err != nil {
		return err
	}

	if err := tmos.EnsureDir(path, 0700); err != nil {
		return err
	}

	// TODO: Define node types, pass a node type as param and get relative config instead
	cfg, err := DefaultFullNodeConfig()
	if err != nil {
		return err
	}

	if err := fsrepo.Init(path, cfg); err != nil {
		return err
	}

	logger.Info("Successfully initialized IPFS repository", "ipfs-path", path)
	return nil
}
