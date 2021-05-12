package p2p

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	ipfscfg "github.com/ipfs/go-ipfs-config"
	ipfscore "github.com/ipfs/go-ipfs/core"
	"github.com/ipfs/go-ipfs/core/node/libp2p"
	"github.com/ipfs/go-ipfs/plugin/loader"
	"github.com/ipfs/go-ipfs/repo/fsrepo"
	"github.com/ipfs/interface-go-ipfs-core/options"
	"github.com/lazyledger/lazyledger-core/libs/log"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"

	tmcfg "github.com/lazyledger/lazyledger-core/config"
	tmos "github.com/lazyledger/lazyledger-core/libs/os"
)

var ErrIPFSIsAlreadyInit = errors.New("ipfs repo was already initialized")

// InitIpfs takes a few config flags from the tendermint config.IPFS
// and applies them to the freshly created IPFS repo.
// The IPFS config will stored under config.IPFS.ConfigRootPath.
// TODO(ismail) move into separate file, and consider making IPFS initialization
// independent from the `tendermint init` subcommand.
// TODO(ismail): add counter part in ResetAllCmd
func InitIpfs(repoRoot string, transformers ...ipfscfg.Transformer) error {
	if fsrepo.IsInitialized(repoRoot) {
		return ErrIPFSIsAlreadyInit
	}
	var conf *ipfscfg.Config

	identity, err := ipfscfg.CreateIdentity(os.Stdout, []options.KeyGenerateOption{
		options.Key.Type(options.Ed25519Key),
	})
	if err != nil {
		return err
	}

	if err := tmos.EnsureDir(repoRoot, 0700); err != nil {
		return err
	}

	conf, err = ipfscfg.InitWithIdentity(identity)
	if err != nil {
		return err
	}

	for _, transformer := range transformers {
		if err := transformer(conf); err != nil {
			return err
		}
	}

	if err := SetupPlugins(repoRoot); err != nil {
		return err
	}
	if err := fsrepo.Init(repoRoot, conf); err != nil {
		return err
	}
	return nil
}

func ApplyBadgerSpec(c *ipfscfg.Config) error {
	c.Datastore.Spec = badgerSpec()
	return nil
}

func badgerSpec() map[string]interface{} {
	return map[string]interface{}{
		"type":   "measure",
		"prefix": "badger.datastore",
		"child": map[string]interface{}{
			"type":       "badgerds",
			"path":       "badgerds",
			"syncWrites": false,
			"truncate":   true,
		},
	}
}

// Inject replies on several global vars internally.
// For instance fsrepo.AddDatastoreConfigHandler will error
// if called multiple times with the same datastore.
// But for CI and integration tests, we want to setup the plugins
// for each repo but only inject once s.t. we can init multiple
// repos from the same runtime.
// TODO(ismail): find a more elegant way to achieve the same.
var injectPluginsOnce sync.Once

func SetupPlugins(path string) error {
	// Load plugins. This will skip the repo if not available.
	plugins, err := loader.NewPluginLoader(filepath.Join(path, "plugins"))
	if err != nil {
		return fmt.Errorf("error loading plugins: %s", err)
	}

	if err := plugins.Initialize(); err != nil {
		return fmt.Errorf("error initializing plugins: %s", err)
	}

	injectPluginsOnce.Do(func() {
		err = plugins.Inject()
	})
	if err != nil {
		return fmt.Errorf("error injecting plugins once: %w", err)
	}

	return nil
}

func CreateIpfsNode(repoRoot string, arePluginsAlreadyLoaded bool, logger log.Logger) (*ipfscore.IpfsNode, error) {
	logger.Info("creating node in repo", "ipfs-root", repoRoot)
	if !fsrepo.IsInitialized(repoRoot) {
		// TODO: sentinel err
		return nil, fmt.Errorf("ipfs repo root: %v not intitialized", repoRoot)
	}
	if !arePluginsAlreadyLoaded {
		if err := setupPlugins(repoRoot, logger); err != nil {
			return nil, err
		}
	}
	// Open the repo
	repo, err := fsrepo.Open(repoRoot)
	if err != nil {
		return nil, err
	}

	// Construct the node
	nodeOptions := &ipfscore.BuildCfg{
		Online: true,
		// This option sets the node to be a full DHT node (both fetching and storing DHT Records)
		Routing: libp2p.DHTOption,
		// This option sets the node to be a client DHT node (only fetching records)
		// Routing: libp2p.DHTClientOption,
		Repo: repo,
	}
	// Internally, ipfs decorates the context with a
	// context.WithCancel. Which is then used for lifecycle management.
	// We do not make use of this context and rely on calling
	// Close() on the node instead
	ctx := context.Background()
	node, err := ipfscore.NewNode(ctx, nodeOptions)
	if err != nil {
		return nil, err
	}
	// run as daemon:
	node.IsDaemon = true
	return node, nil
}

func setupPlugins(path string, logger log.Logger) error {
	// Load plugins. This will skip the repo if not available.
	plugins, err := loader.NewPluginLoader(filepath.Join(path, "plugins"))
	if err != nil {
		return fmt.Errorf("error loading plugins: %s", err)
	}
	if err := plugins.Load(&nodes.LazyLedgerPlugin{}); err != nil {
		return fmt.Errorf("error loading lazyledger plugin: %s", err)
	}
	if err := plugins.Initialize(); err != nil {
		return fmt.Errorf("error initializing plugins: plugins.Initialize(): %s", err)
	}
	if err := plugins.Inject(); err != nil {
		logger.Error("error initializing plugins: could not Inject()", "err", err)
	}

	return nil
}

func applyFromTmConfig(ipfsConf *ipfscfg.Config, tmConf *tmcfg.IPFSConfig) {
	ipfsConf.Addresses.API = ipfscfg.Strings{tmConf.API}
	ipfsConf.Addresses.Gateway = ipfscfg.Strings{tmConf.Gateway}
	ipfsConf.Addresses.Swarm = tmConf.Swarm
	ipfsConf.Addresses.Announce = tmConf.Announce
	ipfsConf.Addresses.NoAnnounce = tmConf.NoAnnounce
}
