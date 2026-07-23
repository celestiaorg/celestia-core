package privval

import (
	"errors"
	"fmt"
	"time"

	"github.com/cometbft/cometbft/libs/trace"
	"github.com/cometbft/cometbft/libs/trace/schema"

	"github.com/cometbft/cometbft/crypto"
	cryptoenc "github.com/cometbft/cometbft/crypto/encoding"
	privvalproto "github.com/cometbft/cometbft/proto/tendermint/privval"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

const (
	failureReasonRemoteError        = "remote_error"
	failureReasonTimeout            = "timeout"
	failureReasonNoConnection       = "no_connection"
	failureReasonUnexpectedResponse = "unexpected_response"
	failureReasonOther              = "other"
)

// SignerClient implements PrivValidator.
// Handles remote validator connections that provide signing services
type SignerClient struct {
	endpoint *SignerListenerEndpoint
	chainID  string
	tracer   trace.Tracer
	metrics  *Metrics
	latency  *signingLatencyTracker
}

var _ types.PrivValidator = (*SignerClient)(nil)

// NewSignerClient returns an instance of SignerClient.
// it will start the endpoint (if not already started)
func NewSignerClient(endpoint *SignerListenerEndpoint, chainID string) (*SignerClient, error) {
	if !endpoint.IsRunning() {
		if err := endpoint.Start(); err != nil {
			return nil, fmt.Errorf("failed to start listener endpoint: %w", err)
		}
	}

	metrics := NopMetrics()
	return &SignerClient{
		endpoint: endpoint,
		chainID:  chainID,
		tracer:   trace.NoOpTracer(),
		metrics:  metrics,
		latency:  newSigningLatencyTracker(metrics, endpoint.Logger),
	}, nil
}

// SetTracer sets the tracer for the SignerClient
func (sc *SignerClient) SetTracer(tracer trace.Tracer) {
	sc.tracer = tracer
}

// SetMetrics sets the metrics sink used to report remote signing latency.
func (sc *SignerClient) SetMetrics(metrics *Metrics) {
	if metrics == nil {
		metrics = NopMetrics()
	}
	sc.metrics = metrics
	sc.latency.setMetrics(metrics)
}

// Close closes the underlying connection
func (sc *SignerClient) Close() error {
	return sc.endpoint.Close()
}

// IsConnected indicates with the signer is connected to a remote signing service
func (sc *SignerClient) IsConnected() bool {
	return sc.endpoint.IsConnected()
}

// WaitForConnection waits maxWait for a connection or returns a timeout error
func (sc *SignerClient) WaitForConnection(maxWait time.Duration) error {
	return sc.endpoint.WaitForConnection(maxWait)
}

//--------------------------------------------------------
// Implement PrivValidator

// Ping sends a ping request to the remote signer
func (sc *SignerClient) Ping() error {
	response, err := sc.endpoint.SendRequest(mustWrapMsg(&privvalproto.PingRequest{}))
	if err != nil {
		sc.endpoint.Logger.Error("SignerClient::Ping", "err", err)
		return nil
	}

	pb := response.GetPingResponse()
	if pb == nil {
		return err
	}

	return nil
}

// GetPubKey retrieves a public key from a remote signer
// returns an error if client is not able to provide the key
func (sc *SignerClient) GetPubKey() (crypto.PubKey, error) {
	response, err := sc.endpoint.SendRequest(mustWrapMsg(&privvalproto.PubKeyRequest{ChainId: sc.chainID}))
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	resp := response.GetPubKeyResponse()
	if resp == nil {
		return nil, ErrUnexpectedResponse
	}
	if resp.Error != nil {
		return nil, &RemoteSignerError{Code: int(resp.Error.Code), Description: resp.Error.Description}
	}

	pk, err := cryptoenc.PubKeyFromProto(resp.PubKey)
	if err != nil {
		return nil, err
	}

	return pk, nil
}

// SignVote requests a remote signer to sign a vote
func (sc *SignerClient) SignVote(chainID string, vote *cmtproto.Vote) error {
	msgType := voteMessageType(vote.Type)
	reqStartTime := time.Now()
	response, err := sc.endpoint.SendRequest(mustWrapMsg(&privvalproto.SignVoteRequest{Vote: vote, ChainId: chainID}))
	reqTime := time.Since(reqStartTime)
	if err != nil {
		sc.recordSigningFailure(msgType, err)
		return err
	}

	resp := response.GetSignedVoteResponse()
	if resp == nil {
		sc.recordSigningFailure(msgType, ErrUnexpectedResponse)
		return ErrUnexpectedResponse
	}
	if resp.Error != nil {
		err := &RemoteSignerError{Code: int(resp.Error.Code), Description: resp.Error.Description}
		sc.recordSigningFailure(msgType, err)
		return err
	}
	switch vote.Type {
	case cmtproto.PrevoteType:
		schema.WriteSignatureLatency(sc.tracer, vote.Height, vote.Round, reqTime.Nanoseconds(), schema.PrevoteType)
		sc.recordSigningLatency(messageTypePrevote, reqTime)
	case cmtproto.PrecommitType:
		schema.WriteSignatureLatency(sc.tracer, vote.Height, vote.Round, reqTime.Nanoseconds(), schema.PrecommitType)
		sc.recordSigningLatency(messageTypePrecommit, reqTime)
	}

	*vote = resp.Vote

	return nil
}

// SignProposal requests a remote signer to sign a proposal
func (sc *SignerClient) SignProposal(chainID string, proposal *cmtproto.Proposal) error {
	reqStartTime := time.Now()
	response, err := sc.endpoint.SendRequest(mustWrapMsg(
		&privvalproto.SignProposalRequest{Proposal: proposal, ChainId: chainID},
	))
	reqTime := time.Since(reqStartTime)
	if err != nil {
		sc.recordSigningFailure(messageTypeProposal, err)
		return err
	}

	resp := response.GetSignedProposalResponse()
	if resp == nil {
		sc.recordSigningFailure(messageTypeProposal, ErrUnexpectedResponse)
		return ErrUnexpectedResponse
	}
	if resp.Error != nil {
		err := &RemoteSignerError{Code: int(resp.Error.Code), Description: resp.Error.Description}
		sc.recordSigningFailure(messageTypeProposal, err)
		return err
	}
	schema.WriteSignatureLatency(sc.tracer, proposal.Height, proposal.Round, reqTime.Nanoseconds(), schema.ProposalType)
	sc.recordSigningLatency(messageTypeProposal, reqTime)

	*proposal = resp.Proposal

	return nil
}

func (sc *SignerClient) SignRawBytes(chainID, uniqueID string, rawBytes []byte) ([]byte, error) {
	reqStartTime := time.Now()
	response, err := sc.endpoint.SendRequest(mustWrapMsg(&privvalproto.SignRawBytesRequest{
		ChainId:  chainID,
		RawBytes: rawBytes,
		UniqueId: uniqueID,
	}))
	reqTime := time.Since(reqStartTime)
	if err != nil {
		sc.recordSigningFailure(messageTypeRawBytes, err)
		return nil, err
	}

	resp := response.GetSignedRawBytesResponse()
	if resp == nil {
		sc.recordSigningFailure(messageTypeRawBytes, ErrUnexpectedResponse)
		return nil, ErrUnexpectedResponse
	}
	if resp.Error != nil {
		err := &RemoteSignerError{Code: int(resp.Error.Code), Description: resp.Error.Description}
		sc.recordSigningFailure(messageTypeRawBytes, err)
		return nil, err
	}
	schema.WriteSignatureLatency(sc.tracer, -1, -1, reqTime.Nanoseconds(), uniqueID)
	sc.recordSigningLatency(messageTypeRawBytes, reqTime)

	return resp.Signature, nil
}

func (sc *SignerClient) recordSigningLatency(messageType string, latency time.Duration) {
	sc.metrics.SigningLatencySeconds.With("message_type", messageType).Observe(latency.Seconds())
	sc.latency.Record(latency)
}

func (sc *SignerClient) recordSigningFailure(messageType string, err error) {
	sc.metrics.SigningFailuresTotal.With(
		"message_type", messageType,
		"reason", failureReason(err),
	).Add(1)
}

func failureReason(err error) string {
	var rse *RemoteSignerError
	switch {
	case errors.As(err, &rse):
		return failureReasonRemoteError
	case errors.Is(err, ErrReadTimeout),
		errors.Is(err, ErrWriteTimeout),
		errors.Is(err, ErrConnectionTimeout):
		return failureReasonTimeout
	case errors.Is(err, ErrNoConnection):
		return failureReasonNoConnection
	case errors.Is(err, ErrUnexpectedResponse):
		return failureReasonUnexpectedResponse
	default:
		return failureReasonOther
	}
}

func voteMessageType(t cmtproto.SignedMsgType) string {
	switch t {
	case cmtproto.PrevoteType:
		return messageTypePrevote
	case cmtproto.PrecommitType:
		return messageTypePrecommit
	default:
		return "vote"
	}
}
