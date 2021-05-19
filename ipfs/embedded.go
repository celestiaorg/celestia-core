package ipfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	ipfscfg "github.com/ipfs/go-ipfs-config"
	"github.com/ipfs/go-ipfs/commands"
	"github.com/ipfs/go-ipfs/core"
	"github.com/ipfs/go-ipfs/core/coreapi"
	"github.com/ipfs/go-ipfs/core/corehttp"
	"github.com/ipfs/go-ipfs/core/node/libp2p"
	"github.com/ipfs/go-ipfs/repo"
	"github.com/ipfs/go-ipfs/repo/fsrepo"
	coreiface "github.com/ipfs/interface-go-ipfs-core"
	"github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/lazyledger/lazyledger-core/libs/log"
)

// Embedded is the provider that embeds IPFS node within the same process.
// It also returns closable for graceful node shutdown.
func Embedded(init bool, cfg *Config, logger log.Logger) APIProvider {
	return func() (coreiface.CoreAPI, io.Closer, error) {
		path := cfg.Path()
		defer os.Setenv(ipfscfg.EnvDir, path)

		// NOTE: no need to validate the path before
		if err := plugins(path); err != nil {
			return nil, nil, err
		}
		// Init Repo if requested
		if init {
			if err := InitRepo(path, logger); err != nil {
				return nil, nil, err
			}
		}
		// Open the repo
		repo, err := fsrepo.Open(path)
		if err != nil {
			var nrerr fsrepo.NoRepoError
			if errors.As(err, &nrerr) {
				return nil, nil, fmt.Errorf("no IPFS repo found in %s.\nplease use flag: --ipfs-init", nrerr.Path)
			}
			return nil, nil, err
		}
		// Construct the node
		nodeOptions := &core.BuildCfg{
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
		// It is essential that we create a fresh instance of ipfs node on
		// each start as internally the node gets only stopped once per instance.
		// At least in ipfs 0.7.0; see:
		// https://github.com/lazyledger/go-ipfs/blob/dd295e45608560d2ada7d7c8a30f1eef3f4019bb/core/builder.go#L48-L57
		node, err := core.NewNode(ctx, nodeOptions)
		if err != nil {
			_ = repo.Close()
			return nil, nil, err
		}
		// Serve API if requested
		if cfg.ServeAPI {
			if err := serveAPI(path, repo, node); err != nil {
				_ = node.Close()
				return nil, nil, err
			}
		}
		// Wrap Node and create CoreAPI
		api, err := coreapi.NewCoreAPI(node)
		if err != nil {
			_ = node.Close()
			return nil, nil, fmt.Errorf("failed to create an instance of the IPFS core API: %w", err)
		}

		logger.Info("Successfully created embedded IPFS node", "ipfs-repo", path)
		return api, node, nil
	}
}

// serveAPI creates and HTTP server for IPFS API.
func serveAPI(path string, repo repo.Repo, node *core.IpfsNode) error {
	cfg, err := repo.Config()
	if err != nil {
		return err
	}
	// go through every configured API address and start to listen to them
	listeners := make([]manet.Listener, len(cfg.Addresses.API))
	for i, addr := range cfg.Addresses.API {
		apiMaddr, err := multiaddr.NewMultiaddr(addr)
		if err != nil {
			return fmt.Errorf("invalid IPFS API address: %q (err: %w)", addr, err)
		}

		listeners[i], err = manet.Listen(apiMaddr)
		if err != nil {
			return fmt.Errorf("listen(%s) for IPFS API failed: %w", apiMaddr, err)
		}
	}

	for _, listener := range listeners {
		fmt.Printf("IPFS API server listening on %s\n", listener.Multiaddr())
	}
	// notify IPFS Repo about running api, it will create a file with an address to simplify access through CLI
	if len(listeners) > 0 {
		if err := node.Repo.SetAPIAddr(listeners[0].Multiaddr()); err != nil {
			return fmt.Errorf("repo.SetAPIAddr() for IPFS API failed: %w", err)
		}
	}
	// configure HTTP server with options
	var opts = []corehttp.ServeOption{
		corehttp.CommandsOption(commands.Context{
			ConfigRoot: path,
			LoadConfig: func(string) (*ipfscfg.Config, error) {
				return cfg, nil
			},
			ConstructNode: func() (*core.IpfsNode, error) {
				return node, nil
			},
			ReqLog: &commands.ReqLog{},
		}),
		corehttp.CheckVersionOption(),
		corehttp.HostnameOption(),
	}

	errc := make(chan error)
	var wg sync.WaitGroup
	for _, apiLis := range listeners {
		wg.Add(1)
		go func(lis manet.Listener) {
			defer wg.Done()
			errc <- corehttp.Serve(node, manet.NetListener(lis), opts...)
		}(apiLis)
	}

	go func() {
		wg.Wait()
		close(errc)
	}()

	return err
}
