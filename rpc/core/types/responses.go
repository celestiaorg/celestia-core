package coretypes

import (
	"encoding/json"
	"time"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/bytes"
	"github.com/cometbft/cometbft/p2p"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

// List of blocks
type ResultBlockchainInfo struct {
	LastHeight int64              `json:"last_height"`
	BlockMetas []*types.BlockMeta `json:"block_metas"`
}

// Genesis file
type ResultGenesis struct {
	Genesis *types.GenesisDoc `json:"genesis"`
}

// ResultGenesisChunk is the output format for the chunked/paginated
// interface. These chunks are produced by converting the genesis
// document to JSON and then splitting the resulting payload into
// 16 megabyte blocks and then base64 encoding each block.
type ResultGenesisChunk struct {
	ChunkNumber int    `json:"chunk"`
	TotalChunks int    `json:"total"`
	Data        string `json:"data"`
}

// Single block (with meta)
type ResultBlock struct {
	BlockID types.BlockID `json:"block_id"`
	Block   *types.Block  `json:"block"`
}

// ResultHeader represents the response for a Header RPC Client query
type ResultHeader struct {
	Header *types.Header `json:"header"`
}

// Commit and Header
type ResultCommit struct {
	types.SignedHeader `json:"signed_header"`
	CanonicalCommit    bool `json:"canonical"`
}

// ABCI results from a block
type ResultBlockResults struct {
	Height                int64                     `json:"height"`
	TxsResults            []*abci.ExecTxResult      `json:"txs_results"`
	FinalizeBlockEvents   []abci.Event              `json:"finalize_block_events"`
	ValidatorUpdates      []abci.ValidatorUpdate    `json:"validator_updates"`
	ConsensusParamUpdates *cmtproto.ConsensusParams `json:"consensus_param_updates"`
	AppHash               []byte                    `json:"app_hash"`
}

// NewResultCommit is a helper to initialize the ResultCommit with
// the embedded struct
func NewResultCommit(header *types.Header, commit *types.Commit,
	canonical bool) *ResultCommit {

	return &ResultCommit{
		SignedHeader: types.SignedHeader{
			Header: header,
			Commit: commit,
		},
		CanonicalCommit: canonical,
	}
}

// Info about the node's syncing state
type SyncInfo struct {
	LatestBlockHash   bytes.HexBytes `json:"latest_block_hash"`
	LatestAppHash     bytes.HexBytes `json:"latest_app_hash"`
	LatestBlockHeight int64          `json:"latest_block_height"`
	LatestBlockTime   time.Time      `json:"latest_block_time"`

	EarliestBlockHash   bytes.HexBytes `json:"earliest_block_hash"`
	EarliestAppHash     bytes.HexBytes `json:"earliest_app_hash"`
	EarliestBlockHeight int64          `json:"earliest_block_height"`
	EarliestBlockTime   time.Time      `json:"earliest_block_time"`

	CatchingUp bool `json:"catching_up"`
}

// Info about the node's validator
type ValidatorInfo struct {
	Address     bytes.HexBytes `json:"address"`
	PubKey      crypto.PubKey  `json:"pub_key"`
	VotingPower int64          `json:"voting_power"`
}

// Node Status
type ResultStatus struct {
	NodeInfo      p2p.DefaultNodeInfo `json:"node_info"`
	SyncInfo      SyncInfo            `json:"sync_info"`
	ValidatorInfo ValidatorInfo       `json:"validator_info"`
}

// Is TxIndexing enabled
func (s *ResultStatus) TxIndexEnabled() bool {
	if s == nil {
		return false
	}
	return s.NodeInfo.Other.TxIndex == "on"
}

// Info about peer connections
type ResultNetInfo struct {
	Listening bool     `json:"listening"`
	Listeners []string `json:"listeners"`
	NPeers    int      `json:"n_peers"`
	Peers     []Peer   `json:"peers"`
}

// Log from dialing seeds
type ResultDialSeeds struct {
	Log string `json:"log"`
}

// Log from dialing peers
type ResultDialPeers struct {
	Log string `json:"log"`
}

// A peer
type Peer struct {
	NodeInfo         p2p.DefaultNodeInfo  `json:"node_info"`
	IsOutbound       bool                 `json:"is_outbound"`
	ConnectionStatus p2p.ConnectionStatus `json:"connection_status"`
	RemoteIP         string               `json:"remote_ip"`
}

// Validators for a height.
type ResultValidators struct {
	BlockHeight int64              `json:"block_height"`
	Validators  []*types.Validator `json:"validators"`
	// Count of actual validators in this result
	Count int `json:"count"`
	// Total number of validators
	Total int `json:"total"`
}

// ConsensusParams for given height
type ResultConsensusParams struct {
	BlockHeight     int64                 `json:"block_height"`
	ConsensusParams types.ConsensusParams `json:"consensus_params"`
}

// Info about the consensus state.
// UNSTABLE
type ResultDumpConsensusState struct {
	RoundState json.RawMessage `json:"round_state"`
	Peers      []PeerStateInfo `json:"peers"`
}

// UNSTABLE
type PeerStateInfo struct {
	NodeAddress string          `json:"node_address"`
	PeerState   json.RawMessage `json:"peer_state"`
}

// UNSTABLE
type ResultConsensusState struct {
	RoundState json.RawMessage `json:"round_state"`
}

// CheckTx result
type ResultBroadcastTx struct {
	Code      uint32         `json:"code"`
	Data      bytes.HexBytes `json:"data"`
	Log       string         `json:"log"`
	Codespace string         `json:"codespace"`

	Hash bytes.HexBytes `json:"hash"`
}

// CheckTx and ExecTx results
type ResultBroadcastTxCommit struct {
	CheckTx  abci.ResponseCheckTx `json:"check_tx"`
	TxResult abci.ExecTxResult    `json:"tx_result"`
	Hash     bytes.HexBytes       `json:"hash"`
	Height   int64                `json:"height"`
}

// ResultCheckTx wraps abci.ResponseCheckTx.
type ResultCheckTx struct {
	abci.ResponseCheckTx
}

// Result of querying for a tx
type ResultTx struct {
	Hash     bytes.HexBytes    `json:"hash"`
	Height   int64             `json:"height"`
	Index    uint32            `json:"index"`
	TxResult abci.ExecTxResult `json:"tx_result"`
	Tx       types.Tx          `json:"tx"`
	Proof    types.ShareProof  `json:"proof,omitempty"`
}

// Result of searching for txs
type ResultTxSearch struct {
	Txs        []*ResultTx `json:"txs"`
	TotalCount int         `json:"total_count"`
}

// ResultBlockSearch defines the RPC response type for a block search by events.
type ResultBlockSearch struct {
	Blocks     []*ResultBlock `json:"blocks"`
	TotalCount int            `json:"total_count"`
}

// List of mempool txs
type ResultUnconfirmedTxs struct {
	Count      int        `json:"n_txs"`
	Total      int        `json:"total"`
	TotalBytes int64      `json:"total_bytes"`
	Txs        []types.Tx `json:"txs"`
}

// Info abci msg
type ResultABCIInfo struct {
	Response abci.ResponseInfo `json:"response"`
}

// Query abci msg
type ResultABCIQuery struct {
	Response abci.ResponseQuery `json:"response"`
}

// Result of broadcasting evidence
type ResultBroadcastEvidence struct {
	Hash []byte `json:"hash"`
}

// empty results
type (
	ResultUnsafeFlushMempool struct{}
	ResultUnsafeProfile      struct{}
	ResultSubscribe          struct{}
	ResultUnsubscribe        struct{}
	ResultHealth             struct{}
)

// Event data from a subscription
type ResultEvent struct {
	Query  string              `json:"query"`
	Data   types.TMEventData   `json:"data"`
	Events map[string][]string `json:"events"`
}

// Single block with all data for validation
type ResultSignedBlock struct {
	Header       types.Header       `json:"header"`
	Commit       types.Commit       `json:"commit"`
	Data         types.Data         `json:"data"`
	ValidatorSet types.ValidatorSet `json:"validator_set"`
}

// ResultTxStatus represents the status of a transaction during its life cycle.
// It contains info to locate a tx in a committed block as well as its execution code, log if it fails and status.
type ResultTxStatus struct {
	// Height is the block height where the transaction was committed. Only populated for COMMITTED status.
	Height int64 `json:"height"`
	// Index is the index of the transaction in the block. Only populated for COMMITTED status.
	Index uint32 `json:"index"`
	// ExecutionCode is the ABCI code returned by the application (0 = success, non-zero = error).
	// Populated for REJECTED and COMMITTED statuses.
	ExecutionCode uint32 `json:"execution_code"`
	// Error contains the error message if the transaction failed. Populated for REJECTED and failed COMMITTED transactions.
	Error string `json:"error"`
	// Status represents the current state of the transaction. Possible values:
	// - UNKNOWN: Transaction not found (never submitted, expired from cache, or submitted to different node)
	// - PENDING: Transaction is in the mempool awaiting inclusion
	// - EVICTED: Transaction was removed from mempool due to space constraints
	// - REJECTED: Transaction was rejected by the application (e.g., during recheck)
	// - COMMITTED: Transaction was included in a block
	Status string `json:"status"`
	// Codespace is the ABCI codespace for the error. Only populated for failed transactions.
	Codespace string `json:"codespace,omitempty"`
	// GasWanted is the maximum gas requested by the transaction. Only populated for COMMITTED status.
	GasWanted int64 `json:"gas_wanted,omitempty"`
	// GasUsed is the actual gas consumed during execution. Only populated for COMMITTED status.
	GasUsed int64 `json:"gas_used,omitempty"`
	// Signers contains the list of addresses that signed the transaction. Only populated for COMMITTED status.
	Signers []string `json:"signers,omitempty"`
}

type ResultDataCommitment struct {
	DataCommitment bytes.HexBytes `json:"data_commitment"`
}

type ResultDataRootInclusionProof struct {
	Proof merkle.Proof `json:"proof"`
}

// ResultShareProof is an API response that contains a ShareProof.
type ResultShareProof struct {
	ShareProof types.ShareProof `json:"share_proof"`
}
