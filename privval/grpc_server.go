package privval

import (
	"context"

	cryptoenc "github.com/cometbft/cometbft/crypto/encoding"
	"github.com/cometbft/cometbft/libs/log"
	crypto "github.com/cometbft/cometbft/proto/tendermint/crypto"
	privvalproto "github.com/cometbft/cometbft/proto/tendermint/privval"
	"github.com/cometbft/cometbft/types"
)

// PrivValidatorGRPCServer implements the PrivValidatorAPIServer gRPC interface
// by forwarding signing requests to an underlying types.PrivValidator.
type PrivValidatorGRPCServer struct {
	privVal types.PrivValidator
	logger  log.Logger
}

// NewPrivValidatorGRPCServer returns a new gRPC server that wraps the given PrivValidator.
func NewPrivValidatorGRPCServer(
	privVal types.PrivValidator,
	logger log.Logger,
) *PrivValidatorGRPCServer {
	return &PrivValidatorGRPCServer{
		privVal: privVal,
		logger:  logger,
	}
}

// SignRawBytes forwards a raw bytes signing request to the underlying PrivValidator.
func (s *PrivValidatorGRPCServer) SignRawBytes(
	_ context.Context,
	req *privvalproto.SignRawBytesRequest,
) (*privvalproto.SignedRawBytesResponse, error) {
	sig, err := s.privVal.SignRawBytes(req.ChainId, req.UniqueId, req.RawBytes)
	if err != nil {
		s.logger.Error("SignRawBytes failed", "err", err)
		return &privvalproto.SignedRawBytesResponse{
			Signature: []byte{},
			Error: &privvalproto.RemoteSignerError{
				Code:        0,
				Description: err.Error(),
			},
		}, nil
	}

	return &privvalproto.SignedRawBytesResponse{
		Signature: sig,
	}, nil
}

func (s *PrivValidatorGRPCServer) GetPubKey(
	_ context.Context,
	req *privvalproto.PubKeyRequest,
) (*privvalproto.PubKeyResponse, error) {
	pubKey, err := s.privVal.GetPubKey()
	if err != nil {
		s.logger.Error("GetPubKey failed", "err", err)
		return &privvalproto.PubKeyResponse{
			PubKey: crypto.PublicKey{},
			Error: &privvalproto.RemoteSignerError{
				Code:        0,
				Description: err.Error(),
			},
		}, nil
	}

	pk, err := cryptoenc.PubKeyToProto(pubKey)
	if err != nil {
		s.logger.Error("GetPubKey failed", "err", err)
		return &privvalproto.PubKeyResponse{
			PubKey: crypto.PublicKey{},
			Error: &privvalproto.RemoteSignerError{
				Code:        0,
				Description: err.Error(),
			},
		}, nil
	}

	return &privvalproto.PubKeyResponse{
		PubKey: pk,
	}, nil
}
