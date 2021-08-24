package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	dummy "github.com/celestiaorg/celestia-core/abci/example/dummyapp/app"
	cmd "github.com/celestiaorg/celestia-core/cmd/tendermint/commands"
	"github.com/celestiaorg/celestia-core/cmd/tendermint/commands/debug"
	cfg "github.com/celestiaorg/celestia-core/config"
	"github.com/celestiaorg/celestia-core/ipfs"
	"github.com/celestiaorg/celestia-core/libs/cli"
	"github.com/celestiaorg/celestia-core/libs/log"
	"github.com/celestiaorg/celestia-core/node"
	"github.com/celestiaorg/celestia-core/p2p"
	"github.com/celestiaorg/celestia-core/privval"
	"github.com/celestiaorg/celestia-core/proxy"
)

var (
	randTxs  uint32
	txSize   uint32
	msgSize  uint32
	randMsgs uint32

	sleepDuringPreprocess time.Duration

	runNodeCmd = cmd.NewRunNodeCmd(DummyNode)
)

func init() {
	runNodeCmd.Short = "Run dummyapp"

	runNodeCmd.PersistentFlags().DurationVar(
		&sleepDuringPreprocess,
		"dummy.sleep", 250*time.Millisecond,
		"duration to sleep during preprocess phase, e.g. 500ms, 1s")

	runNodeCmd.PersistentFlags().Uint32Var(&randTxs, "dummy.txs", 10, "generate random transactions")
	runNodeCmd.PersistentFlags().Uint32Var(&txSize, "dummy.tx-size", 50, "size of random transactions")

	runNodeCmd.PersistentFlags().Uint32Var(&randMsgs, "dummy.msgs", 16, "generate random messages")
	runNodeCmd.PersistentFlags().Uint32Var(&msgSize, "dummy.msg-size", 128, "size of random messages")
}

func main() {
	// copy of the tendermint rootCmd
	rootCmd := *cmd.RootCmd
	rootCmd.Use = "dummyapp"
	rootCmd.Short = "Simple KVStore running tendermint and optionally generating random data for testing"
	rootCmd.AddCommand(
		cmd.GenValidatorCmd,
		cmd.InitFilesCmd,
		cmd.ProbeUpnpCmd,
		cmd.LightCmd,
		cmd.ReplayCmd,
		cmd.ReplayConsoleCmd,
		cmd.ResetAllCmd,
		cmd.ResetPrivValidatorCmd,
		cmd.ShowValidatorCmd,
		cmd.TestnetFilesCmd,
		cmd.ShowNodeIDCmd,
		cmd.GenNodeKeyCmd,
		cmd.VersionCmd,
		debug.DebugCmd,
		cli.NewCompletionCmd(&rootCmd, true),
	)

	// Create & start node
	rootCmd.AddCommand(runNodeCmd)

	cmd := cli.PrepareBaseCmd(&rootCmd, "TM", os.ExpandEnv(filepath.Join("$HOME", cfg.DefaultTendermintDir)))
	if err := cmd.Execute(); err != nil {
		panic(err)
	}
}

// DummyNode implements NodeProvider.
func DummyNode(config *cfg.Config, logger log.Logger) (*node.Node, error) {
	if err := ipfs.InitRepo(config.IPFS.Path(), logger); err != nil {
		return nil, fmt.Errorf("failed to initialize IPFS repo at path %s: %v", config.IPFS.Path(), err)
	}

	nodeKey, err := p2p.LoadOrGenNodeKey(config.NodeKeyFile())
	if err != nil {
		return nil, fmt.Errorf("failed to load or gen node key %s: %w", config.NodeKeyFile(), err)
	}

	pval, err := privval.LoadOrGenFilePV(config.PrivValidatorKeyFile(), config.PrivValidatorStateFile())
	if err != nil {
		return nil, err
	}
	app := dummy.NewApplication(
		dummy.RandMessagesOnPreprocess(randTxs, txSize, randMsgs, msgSize),
		dummy.SleepDuration(sleepDuringPreprocess),
	)

	return node.NewNode(config,
		pval,
		nodeKey,
		proxy.NewLocalClientCreator(app),
		node.DefaultGenesisDocProviderFunc(config),
		node.DefaultDBProvider,
		node.DefaultMetricsProvider(config.Instrumentation),
		logger,
	)
}
