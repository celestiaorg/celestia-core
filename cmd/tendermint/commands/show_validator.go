package commands

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/celestiaorg/celestia-core/crypto"
	tmjson "github.com/celestiaorg/celestia-core/libs/json"
	tmnet "github.com/celestiaorg/celestia-core/libs/net"
	tmos "github.com/celestiaorg/celestia-core/libs/os"
	"github.com/celestiaorg/celestia-core/privval"
	tmgrpc "github.com/celestiaorg/celestia-core/privval/grpc"
)

// ShowValidatorCmd adds capabilities for showing the validator info.
var ShowValidatorCmd = &cobra.Command{
	Use:   "show-validator",
	Short: "Show this node's validator info",
	RunE:  showValidator,
}

func showValidator(cmd *cobra.Command, args []string) error {
	var (
		pubKey crypto.PubKey
		err    error
	)

	//TODO: remove once gRPC is the only supported protocol
	protocol, _ := tmnet.ProtocolAndAddress(config.PrivValidator.ListenAddr)
	switch protocol {
	case "grpc":
		pvsc, err := tmgrpc.DialRemoteSigner(
			config.PrivValidator,
			config.ChainID(),
			logger,
			config.Instrumentation.Prometheus,
		)
		if err != nil {
			return fmt.Errorf("can't connect to remote validator %w", err)
		}

		ctx, cancel := context.WithTimeout(context.TODO(), ctxTimeout)
		defer cancel()

		pubKey, err = pvsc.GetPubKey(ctx)
		if err != nil {
			return fmt.Errorf("can't get pubkey: %w", err)
		}
	default:

		keyFilePath := config.PrivValidator.KeyFile()
		if !tmos.FileExists(keyFilePath) {
			return fmt.Errorf("private validator file %s does not exist", keyFilePath)
		}

		pv, err := privval.LoadFilePV(keyFilePath, config.PrivValidator.StateFile())
		if err != nil {
			return err
		}

		ctx, cancel := context.WithTimeout(context.TODO(), ctxTimeout)
		defer cancel()

		pubKey, err = pv.GetPubKey(ctx)
		if err != nil {
			return fmt.Errorf("can't get pubkey: %w", err)
		}
	}

	bz, err := tmjson.Marshal(pubKey)
	if err != nil {
		return fmt.Errorf("failed to marshal private validator pubkey: %w", err)
	}

	fmt.Println(string(bz))
	return nil
}
