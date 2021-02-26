package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	ipfscfg "github.com/ipfs/go-ipfs-config"
	"github.com/ipfs/go-ipfs/plugin/loader"
	"github.com/ipfs/go-ipfs/repo/fsrepo"
	"github.com/ipfs/interface-go-ipfs-core/options"
	"github.com/spf13/cobra"

	cfg "github.com/lazyledger/lazyledger-core/config"
	tmos "github.com/lazyledger/lazyledger-core/libs/os"
	tmrand "github.com/lazyledger/lazyledger-core/libs/rand"
	"github.com/lazyledger/lazyledger-core/p2p"
	"github.com/lazyledger/lazyledger-core/privval"
	tmproto "github.com/lazyledger/lazyledger-core/proto/tendermint/types"
	"github.com/lazyledger/lazyledger-core/types"
	tmtime "github.com/lazyledger/lazyledger-core/types/time"
)

// InitFilesCmd initialises a fresh Tendermint Core instance.
var InitFilesCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize Tendermint",
	RunE:  initFiles,
}

var (
	keyType string
)

func init() {
	InitFilesCmd.Flags().StringVar(&keyType, "key", types.ABCIPubKeyTypeEd25519,
		"Key type to generate privval file with. Options: ed25519, secp256k1")
}

func initFiles(cmd *cobra.Command, args []string) error {
	return initFilesWithConfig(config)
}

func initFilesWithConfig(config *cfg.Config) error {
	// private validator
	privValKeyFile := config.PrivValidatorKeyFile()
	privValStateFile := config.PrivValidatorStateFile()
	var (
		pv  *privval.FilePV
		err error
	)
	if tmos.FileExists(privValKeyFile) {
		pv = privval.LoadFilePV(privValKeyFile, privValStateFile)
		logger.Info("Found private validator", "keyFile", privValKeyFile,
			"stateFile", privValStateFile)
	} else {
		pv, err = privval.GenFilePV(privValKeyFile, privValStateFile, keyType)
		if err != nil {
			return err
		}
		pv.Save()
		logger.Info("Generated private validator", "keyFile", privValKeyFile,
			"stateFile", privValStateFile)
	}

	nodeKeyFile := config.NodeKeyFile()
	if tmos.FileExists(nodeKeyFile) {
		logger.Info("Found node key", "path", nodeKeyFile)
	} else {
		if _, err := p2p.LoadOrGenNodeKey(nodeKeyFile); err != nil {
			return err
		}
		logger.Info("Generated node key", "path", nodeKeyFile)
	}

	// genesis file
	genFile := config.GenesisFile()
	if tmos.FileExists(genFile) {
		logger.Info("Found genesis file", "path", genFile)
	} else {

		genDoc := types.GenesisDoc{
			ChainID:         fmt.Sprintf("test-chain-%v", tmrand.Str(6)),
			GenesisTime:     tmtime.Now(),
			ConsensusParams: types.DefaultConsensusParams(),
		}
		if keyType == "secp256k1" {
			genDoc.ConsensusParams.Validator = tmproto.ValidatorParams{
				PubKeyTypes: []string{types.ABCIPubKeyTypeSecp256k1},
			}
		}
		pubKey, err := pv.GetPubKey()
		if err != nil {
			return fmt.Errorf("can't get pubkey: %w", err)
		}
		genDoc.Validators = []types.GenesisValidator{{
			Address: pubKey.Address(),
			PubKey:  pubKey,
			Power:   10,
		}}

		if err := genDoc.SaveAs(genFile); err != nil {
			return err
		}
		logger.Info("Generated genesis file", "path", genFile)
	}

	if err := InitIpfs(config); err != nil {
		return err
	}

	return nil
}

// InitIpfs takes a few config flags from the tendermint config.IPFS
// and applies them to the freshly created IPFS repo.
// The IPFS config will stored under config.IPFS.ConfigRootPath.
// TODO(ismail) move into separate file, and consider making IPFS initialization
// independent from the `tendermint init` subcommand.
// TODO(ismail): add counter part in ResetAllCmd
func InitIpfs(config *cfg.Config) error {
	repoRoot := config.IPFSRepoRoot()
	if fsrepo.IsInitialized(repoRoot) {
		logger.Info("IPFS was already initialized", "ipfs-path", repoRoot)
		return nil
	}
	var conf *ipfscfg.Config

	identity, err := ipfscfg.CreateIdentity(os.Stdout, []options.KeyGenerateOption{
		options.Key.Type(options.Ed25519Key),
	})
	if err != nil {
		return err
	}

	logger.Info("initializing IPFS node", "ipfs-path", repoRoot)

	if err := tmos.EnsureDir(repoRoot, 0700); err != nil {
		return err
	}

	conf, err = ipfscfg.InitWithIdentity(identity)
	if err != nil {
		return err
	}

	applyFromTmConfig(conf, config.IPFS)
	if err := setupPlugins(repoRoot); err != nil {
		return err
	}

	if err := fsrepo.Init(repoRoot, conf); err != nil {
		return err
	}
	return nil
}

// Inject replies on several global vars internally.
// For instance fsrepo.AddDatastoreConfigHandler will error
// if called multiple times with the same datastore.
// But for CI and integration tests, we want to setup the plugins
// for each repo but only inject once s.t. we can init multiple
// repos from the same runtime.
// TODO(ismail): find a more elegant way to achieve the same.
var injectPluginsOnce sync.Once

func setupPlugins(path string) error {
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

func applyFromTmConfig(ipfsConf *ipfscfg.Config, tmConf *cfg.IPFSConfig) {
	ipfsConf.Addresses.API = ipfscfg.Strings{tmConf.API}
	ipfsConf.Addresses.Gateway = ipfscfg.Strings{tmConf.Gateway}
	ipfsConf.Addresses.Swarm = tmConf.Swarm
	ipfsConf.Addresses.Announce = tmConf.Announce
	ipfsConf.Addresses.NoAnnounce = tmConf.NoAnnounce
}
