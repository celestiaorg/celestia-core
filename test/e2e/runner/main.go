package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/pkg/trace"
	"github.com/tendermint/tendermint/rpc/client/http"
	e2e "github.com/tendermint/tendermint/test/e2e/pkg"
	"github.com/tendermint/tendermint/test/e2e/pkg/infra"
	"github.com/tendermint/tendermint/test/e2e/pkg/infra/docker"
)

var (
	logger = log.NewTMLogger(log.NewSyncWriter(os.Stdout))
)

func main() {
	NewCLI().Run()
}

// CLI is the Cobra-based command-line interface.
type CLI struct {
	root     *cobra.Command
	testnet  *e2e.Testnet
	preserve bool
	infp     infra.Provider
}

// NewCLI sets up the CLI.
func NewCLI() *CLI {
	cli := &CLI{}
	cli.root = &cobra.Command{
		Use:           "runner",
		Short:         "End-to-end test runner",
		SilenceUsage:  true,
		SilenceErrors: true, // we'll output them ourselves in Run()
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			file, err := cmd.Flags().GetString("file")
			if err != nil {
				return err
			}
			m, err := e2e.LoadManifest(file)
			if err != nil {
				return err
			}

			inft, err := cmd.Flags().GetString("infrastructure-type")
			if err != nil {
				return err
			}

			var ifd e2e.InfrastructureData
			switch inft {
			case "docker":
				var err error
				ifd, err = e2e.NewDockerInfrastructureData(m)
				if err != nil {
					return err
				}
			case "digital-ocean":
				p, err := cmd.Flags().GetString("infrastructure-data")
				if err != nil {
					return err
				}
				if p == "" {
					return errors.New("'--infrastructure-data' must be set when using the 'digital-ocean' infrastructure-type")
				}
				ifd, err = e2e.InfrastructureDataFromFile(p)
				if err != nil {
					return fmt.Errorf("parsing infrastructure data: %s", err)
				}
			default:
				return fmt.Errorf("unknown infrastructure type '%s'", inft)
			}

			iurl, err := cmd.Flags().GetString(trace.FlagTracePushConfig)
			if err != nil {
				return err
			}
			itoken, err := cmd.Flags().GetString(trace.FlagTracePullAddress)
			if err != nil {
				return err
			}
			if ifd.TracePushConfig == "" {
				ifd.TracePushConfig = iurl
				ifd.TracePullAddress = itoken
			}

			purl, err := cmd.Flags().GetString(trace.FlagPyroscopeURL)
			if err != nil {
				return err
			}
			pTrace, err := cmd.Flags().GetBool(trace.FlagPyroscopeTrace)
			if err != nil {
				return err
			}
			if ifd.PyroscopeURL == "" {
				ifd.PyroscopeURL = purl
				ifd.PyroscopeTrace = pTrace
			}

			testnet, err := e2e.LoadTestnet(m, file, ifd)
			if err != nil {
				return fmt.Errorf("loading testnet: %s", err)
			}

			cli.testnet = testnet
			cli.infp = &infra.NoopProvider{}
			if inft == "docker" {
				cli.infp = &docker.Provider{Testnet: testnet}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := Cleanup(cli.testnet); err != nil {
				return err
			}
			if err := Setup(cli.testnet, cli.infp); err != nil {
				return err
			}

			chLoadResult := make(chan error)
			ctx, loadCancel := context.WithCancel(context.Background())
			defer loadCancel()
			go func() {
				err := Load(ctx, cli.testnet)
				if err != nil {
					logger.Error(fmt.Sprintf("Transaction load failed: %v", err.Error()))
				}
				chLoadResult <- err
			}()

			if err := Start(cli.testnet); err != nil {
				return err
			}

			if lastMisbehavior := cli.testnet.LastMisbehaviorHeight(); lastMisbehavior > 0 {
				// wait for misbehaviors before starting perturbations. We do a separate
				// wait for another 5 blocks, since the last misbehavior height may be
				// in the past depending on network startup ordering.
				if err := WaitUntil(cli.testnet, lastMisbehavior); err != nil {
					return err
				}
			}
			if err := Wait(cli.testnet, 30); err != nil { // allow some txs to go through
				return err
			}

			if cli.testnet.HasPerturbations() {
				if err := Perturb(cli.testnet); err != nil {
					return err
				}
				if err := Wait(cli.testnet, 5); err != nil { // allow some txs to go through
					return err
				}
			}

			loadCancel()
			if err := <-chLoadResult; err != nil {
				return err
			}
			if err := Wait(cli.testnet, 5); err != nil { // wait for network to settle before tests
				return err
			}
			if err := Test(cli.testnet); err != nil {
				return err
			}
			if !cli.preserve {
				if err := Cleanup(cli.testnet); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cli.root.PersistentFlags().StringP("file", "f", "", "Testnet TOML manifest")
	_ = cli.root.MarkPersistentFlagRequired("file")

	cli.root.PersistentFlags().StringP("infrastructure-type", "", "docker", "Backing infrastructure used to run the testnet. Either 'digital-ocean' or 'docker'")

	cli.root.PersistentFlags().StringP("infrastructure-data", "", "", "path to the json file containing the infrastructure data. Only used if the 'infrastructure-type' is set to a value other than 'docker'")

	cli.root.PersistentFlags().String(trace.FlagTracePushConfig, "", trace.FlagTracePushConfigDescription)

	cli.root.PersistentFlags().String(trace.FlagTracePullAddress, "", trace.FlagTracePullAddressDescription)

	cli.root.PersistentFlags().String(trace.FlagPyroscopeURL, "", trace.FlagPyroscopeURLDescription)

	cli.root.PersistentFlags().Bool(trace.FlagPyroscopeTrace, false, trace.FlagPyroscopeTraceDescription)

	cli.root.Flags().BoolVarP(&cli.preserve, "preserve", "p", false,
		"Preserves the running of the test net after tests are completed")

	cli.root.AddCommand(&cobra.Command{
		Use:   "setup",
		Short: "Generates the testnet directory and configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return Setup(cli.testnet, cli.infp)
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "start",
		Short: "Starts the Docker testnet, waiting for nodes to become available",
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := os.Stat(cli.testnet.Dir)
			if os.IsNotExist(err) {
				err = Setup(cli.testnet, cli.infp)
			}
			if err != nil {
				return err
			}
			return Start(cli.testnet)
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "perturb",
		Short: "Perturbs the Docker testnet, e.g. by restarting or disconnecting nodes",
		RunE: func(cmd *cobra.Command, args []string) error {
			return Perturb(cli.testnet)
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "wait",
		Short: "Waits for a few blocks to be produced and all nodes to catch up",
		RunE: func(cmd *cobra.Command, args []string) error {
			return Wait(cli.testnet, 5)
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "stop",
		Short: "Stops the Docker testnet",
		RunE: func(cmd *cobra.Command, args []string) error {
			logger.Info("Stopping testnet")
			return execCompose(cli.testnet.Dir, "down")
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "load",
		Short: "Generates transaction load until the command is canceled",
		RunE: func(cmd *cobra.Command, args []string) error {
			return Load(context.Background(), cli.testnet)
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "blocktime",
		Short: "Displays the block time for the last 100 blocks",
		RunE: func(cmd *cobra.Command, args []string) error {
			return BlockTime(cmd.Context(), cli.testnet, 100)
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "test",
		Short: "Runs test cases against a running testnet",
		RunE: func(cmd *cobra.Command, args []string) error {
			return Test(cli.testnet)
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "cleanup",
		Short: "Removes the testnet directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			return Cleanup(cli.testnet)
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "logs",
		Short: "Shows the testnet logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return execComposeVerbose(cli.testnet.Dir, "logs")
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "tail",
		Short: "Tails the testnet logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			return execComposeVerbose(cli.testnet.Dir, "logs", "--follow")
		},
	})

	cli.root.AddCommand(&cobra.Command{
		Use:   "benchmark",
		Short: "Benchmarks testnet",
		Long: `Benchmarks the following metrics:
	Mean Block Interval
	Standard Deviation
	Min Block Interval
	Max Block Interval
over a 100 block sampling period.

Does not run any perturbations.
		`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := Cleanup(cli.testnet); err != nil {
				return err
			}
			if err := Setup(cli.testnet, cli.infp); err != nil {
				return err
			}

			chLoadResult := make(chan error)
			ctx, loadCancel := context.WithCancel(context.Background())
			defer loadCancel()
			go func() {
				err := Load(ctx, cli.testnet)
				if err != nil {
					logger.Error(fmt.Sprintf("Transaction load errored: %v", err.Error()))
				}
				chLoadResult <- err
			}()

			if err := Start(cli.testnet); err != nil {
				return err
			}

			if err := Wait(cli.testnet, 5); err != nil { // allow some txs to go through
				return err
			}

			// we benchmark performance over the next 100 blocks
			if err := Benchmark(cli.testnet, 100); err != nil {
				return err
			}

			loadCancel()
			if err := <-chLoadResult; err != nil {
				return err
			}

			return Cleanup(cli.testnet)
		},
	})

	return cli
}

// Run runs the CLI.
func (cli *CLI) Run() {
	if err := cli.root.Execute(); err != nil {
		logger.Error(err.Error())
		os.Exit(1)
	}
}

func BlockTime(ctx context.Context, testnet *e2e.Testnet, numBlocks int) error {
	client, err := testnet.Nodes[0].Client()
	if err != nil {
		return err
	}
	lastPrintedHeight := int64(0)
	var latestHeight int64
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			resp, err := client.Status(ctx)
			if err != nil {
				return err
			}
			latestHeight = resp.SyncInfo.LatestBlockHeight
			if lastPrintedHeight == 0 {
				lastPrintedHeight = latestHeight - 1
			}
			if latestHeight > lastPrintedHeight {
				printBlockTimes(ctx, client, lastPrintedHeight, latestHeight)
				lastPrintedHeight = latestHeight
			}
		}
	}
}

func printBlockTimes(
	ctx context.Context,
	client *http.HTTP,
	fromHeight, toHeight int64,
) error {
	for height := fromHeight; height < toHeight; height++ {
		nextHeight := height + 1
		resp, err := client.Header(ctx, &height)
		if err != nil {
			return err
		}
		firstTime := resp.Header.Time
		resp, err = client.Header(ctx, &nextHeight)
		if err != nil {
			return err
		}
		secondTime := resp.Header.Time
		logger.Info("block time", "height", height, "time", secondTime.Sub(firstTime))
	}
	return nil
}
