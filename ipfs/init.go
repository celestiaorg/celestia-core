package ipfs

import (
	"os"

	ipfscfg "github.com/ipfs/go-ipfs-config"
	"github.com/ipfs/go-ipfs/repo/fsrepo"
	"github.com/ipfs/interface-go-ipfs-core/options"

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

	identity, err := ipfscfg.CreateIdentity(os.Stdout, []options.KeyGenerateOption{
		options.Key.Type(options.Ed25519Key),
	})
	if err != nil {
		return err
	}

	conf, err := ipfscfg.InitWithIdentity(identity)
	if err != nil {
		return err
	}

	if err := fsrepo.Init(path, conf); err != nil {
		return err
	}

	logger.Info("Successfully initialized IPFS repository", "ipfs-path", path)
	return nil
}
