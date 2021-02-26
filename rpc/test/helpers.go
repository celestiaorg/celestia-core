package rpctest

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	ipfscfg "github.com/ipfs/go-ipfs-config"
	"github.com/ipfs/go-ipfs/plugin/loader"
	"github.com/ipfs/go-ipfs/repo/fsrepo"
	"github.com/ipfs/interface-go-ipfs-core/options"
	abci "github.com/lazyledger/lazyledger-core/abci/types"
	"github.com/lazyledger/lazyledger-core/libs/log"
	tmos "github.com/lazyledger/lazyledger-core/libs/os"
	"github.com/lazyledger/lazyledger-core/p2p/ipld/plugin/nodes"

	cfg "github.com/lazyledger/lazyledger-core/config"
	tmnet "github.com/lazyledger/lazyledger-core/libs/net"
	nm "github.com/lazyledger/lazyledger-core/node"
	"github.com/lazyledger/lazyledger-core/p2p"
	"github.com/lazyledger/lazyledger-core/privval"
	"github.com/lazyledger/lazyledger-core/proxy"
	ctypes "github.com/lazyledger/lazyledger-core/rpc/core/types"
	core_grpc "github.com/lazyledger/lazyledger-core/rpc/grpc"
	rpcclient "github.com/lazyledger/lazyledger-core/rpc/jsonrpc/client"
)

// Options helps with specifying some parameters for our RPC testing for greater
// control.
type Options struct {
	suppressStdout  bool
	recreateConfig  bool
	loadIpfsPlugins bool
}

var globalConfig *cfg.Config
var defaultOptions = Options{
	suppressStdout:  false,
	recreateConfig:  false,
	loadIpfsPlugins: true,
}

func waitForRPC() {
	laddr := GetConfig().RPC.ListenAddress
	client, err := rpcclient.New(laddr)
	if err != nil {
		panic(err)
	}
	result := new(ctypes.ResultStatus)
	for {
		_, err := client.Call(context.Background(), "status", map[string]interface{}{}, result)
		if err == nil {
			return
		}

		fmt.Println("error", err)
		time.Sleep(time.Millisecond)
	}
}

func waitForGRPC() {
	client := GetGRPCClient()
	for {
		_, err := client.Ping(context.Background(), &core_grpc.RequestPing{})
		if err == nil {
			return
		}
	}
}

// f**ing long, but unique for each test
func makePathname() string {
	// get path
	p, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	// fmt.Println(p)
	sep := string(filepath.Separator)
	return strings.ReplaceAll(p, sep, "_")
}

func randPort() int {
	port, err := tmnet.GetFreePort()
	if err != nil {
		panic(err)
	}
	return port
}

func makeAddrs() (string, string, string) {
	return fmt.Sprintf("tcp://127.0.0.1:%d", randPort()),
		fmt.Sprintf("tcp://127.0.0.1:%d", randPort()),
		fmt.Sprintf("tcp://127.0.0.1:%d", randPort())
}

func createConfig() *cfg.Config {
	pathname := makePathname()
	c := cfg.ResetTestRoot(pathname)

	// and we use random ports to run in parallel
	tm, rpc, grpc := makeAddrs()
	c.P2P.ListenAddress = tm
	c.RPC.ListenAddress = rpc
	c.RPC.CORSAllowedOrigins = []string{"https://tendermint.com/"}
	c.RPC.GRPCListenAddress = grpc
	return c
}

// GetConfig returns a config for the test cases as a singleton
func GetConfig(forceCreate ...bool) *cfg.Config {
	if globalConfig == nil || (len(forceCreate) > 0 && forceCreate[0]) {
		globalConfig = createConfig()
	}
	return globalConfig
}

func GetGRPCClient() core_grpc.BroadcastAPIClient {
	grpcAddr := globalConfig.RPC.GRPCListenAddress
	return core_grpc.StartGRPCClient(grpcAddr)
}

// StartTendermint starts a test tendermint server in a go routine and returns when it is initialized
func StartTendermint(app abci.Application, opts ...func(*Options)) *nm.Node {
	nodeOpts := defaultOptions
	for _, opt := range opts {
		opt(&nodeOpts)
	}
	node := NewTendermint(app, &nodeOpts)
	err := node.Start()
	if err != nil {
		panic(err)
	}

	// wait for rpc
	waitForRPC()
	waitForGRPC()

	if !nodeOpts.suppressStdout {
		fmt.Println("Tendermint running!")
	}

	return node
}

// StopTendermint stops a test tendermint server, waits until it's stopped and
// cleans up test/config files.
func StopTendermint(node *nm.Node) {
	if err := node.Stop(); err != nil {
		node.Logger.Error("Error when tryint to stop node", "err", err)
	}
	node.Wait()
	os.RemoveAll(node.Config().RootDir)
}

// NewTendermint creates a new tendermint server and sleeps forever
func NewTendermint(app abci.Application, opts *Options) *nm.Node {
	// Create & start node
	config := GetConfig(opts.recreateConfig)
	var logger log.Logger
	if opts.suppressStdout {
		logger = log.NewNopLogger()
	} else {
		logger = log.NewTMLogger(log.NewSyncWriter(os.Stdout))
		logger = log.NewFilter(logger, log.AllowError())
	}
	pvKeyFile := config.PrivValidatorKeyFile()
	pvKeyStateFile := config.PrivValidatorStateFile()
	pv, err := privval.LoadOrGenFilePV(pvKeyFile, pvKeyStateFile)
	if err != nil {
		panic(err)
	}
	papp := proxy.NewLocalClientCreator(app)
	nodeKey, err := p2p.LoadOrGenNodeKey(config.NodeKeyFile())
	if err != nil {
		panic(err)
	}

	config.IPFS = cfg.DefaultIPFSConfig()
	err = initIpfs(config, opts.loadIpfsPlugins, logger)
	if err != nil {
		panic(err)
	}
	node, err := nm.NewNode(config, pv, nodeKey, papp,
		nm.DefaultGenesisDocProviderFunc(config),
		nm.DefaultDBProvider,
		nm.DefaultMetricsProvider(config.Instrumentation),
		logger,
		nm.IpfsPluginsWereLoaded(true),
	)
	if err != nil {
		panic(err)
	}
	return node
}

// initIpfs a slightly modified commands.InitIpfs.
// To avoid depending on the commands package here, we accept some code duplication.
// In case commands.InitIpfs gets refactored into a separate package,
// it could be used instead.
func initIpfs(config *cfg.Config, loadPlugins bool, log log.Logger) error {
	repoRoot := config.IPFSRepoRoot()
	if fsrepo.IsInitialized(repoRoot) {
		log.Info("IPFS repo already initialized", "repo-root", repoRoot)
		return nil
	}
	var conf *ipfscfg.Config

	identity, err := ipfscfg.CreateIdentity(ioutil.Discard, []options.KeyGenerateOption{
		options.Key.Type(options.Ed25519Key),
	})
	if err != nil {
		return err
	}
	if err := tmos.EnsureDir(repoRoot, 0700); err != nil {
		return err
	}
	if loadPlugins {
		plugins, err := loader.NewPluginLoader(filepath.Join(repoRoot, "plugins"))
		if err != nil {
			return fmt.Errorf("error loading plugins: %s", err)
		}
		if err := plugins.Load(&nodes.LazyLedgerPlugin{}); err != nil {
			return err
		}
		if err := plugins.Initialize(); err != nil {
			return fmt.Errorf("error initializing plugins: %s", err)
		}
		if err := plugins.Inject(); err != nil {
			return fmt.Errorf("error initializing plugins: %s", err)
		}
	}
	conf, err = ipfscfg.InitWithIdentity(identity)
	if err != nil {
		return fmt.Errorf("initializing config failed, InitWithIdentity(): %w", err)
	}

	if err := fsrepo.Init(repoRoot, conf); err != nil {
		return err
	}

	return nil
}

// SuppressStdout is an option that tries to make sure the RPC test Tendermint
// node doesn't log anything to stdout.
func SuppressStdout(o *Options) {
	o.suppressStdout = true
}

// RecreateConfig instructs the RPC test to recreate the configuration each
// time, instead of treating it as a global singleton.
func RecreateConfig(o *Options) {
	o.recreateConfig = true
}

// DoNotLoadIpfsPlugins instructs the RPC test to not load the IPFS plugins, e.g.,
// to prevent loading them several times.
func DoNotLoadIpfsPlugins(o *Options) {
	o.loadIpfsPlugins = false
}
