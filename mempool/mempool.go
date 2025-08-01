package mempool

import (
	"crypto/sha256"
	"fmt"
	"math"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/types"
)

const (
	MempoolChannel = byte(0x30)

	// PeerCatchupSleepIntervalMS defines how much time to sleep if a peer is behind
	PeerCatchupSleepIntervalMS = 100

	// UnknownPeerID is the peer ID to use when running CheckTx when there is
	// no peer (e.g. RPC)
	UnknownPeerID uint16 = 0

	MaxActiveIDs = math.MaxUint16
)

//go:generate ../scripts/mockery_generate.sh Mempool

// Mempool defines the mempool interface.
//
// Updates to the mempool need to be synchronized with committing a block so
// applications can reset their transient state on Commit.
type Mempool interface {
	// CheckTx executes a new transaction against the application to determine
	// its validity and whether it should be added to the mempool.
	CheckTx(tx types.Tx, callback func(*abci.ResponseCheckTx), txInfo TxInfo) error

	// RemoveTxByKey removes a transaction, identified by its key,
	// from the mempool.
	RemoveTxByKey(txKey types.TxKey) error

	// ReapMaxBytesMaxGas reaps transactions from the mempool up to maxBytes
	// bytes total with the condition that the total gasWanted must be less than
	// maxGas.
	//
	// If both maxes are negative, there is no cap on the size of all returned
	// transactions (~ all available transactions).
	ReapMaxBytesMaxGas(maxBytes, maxGas int64) []*types.CachedTx

	// ReapMaxTxs reaps up to max transactions from the mempool. If max is
	// negative, there is no cap on the size of all returned transactions
	// (~ all available transactions).
	ReapMaxTxs(max int) []*types.CachedTx

	// Lock locks the mempool. The consensus must be able to hold lock to safely
	// update.
	// Before acquiring the lock, it signals the mempool that a new update is coming.
	// If the mempool is still rechecking at this point, it should be considered full.
	Lock()

	// Unlock unlocks the mempool.
	Unlock()

	// Update informs the mempool that the given txs were committed and can be
	// discarded.
	//
	// NOTE:
	// 1. This should be called *after* block is committed by consensus.
	// 2. Lock/Unlock must be managed by the caller.
	Update(
		blockHeight int64,
		blockTxs []*types.CachedTx,
		deliverTxResponses []*abci.ExecTxResult,
		newPreFn PreCheckFunc,
		newPostFn PostCheckFunc,
	) error

	// FlushAppConn flushes the mempool connection to ensure async callback calls
	// are done, e.g. from CheckTx.
	//
	// NOTE:
	// 1. Lock/Unlock must be managed by caller.
	FlushAppConn() error

	// Flush removes all transactions from the mempool and caches.
	Flush()

	// TxsAvailable returns a channel which fires once for every height, and only
	// when transactions are available in the mempool.
	//
	// NOTE:
	// 1. The returned channel may be nil if EnableTxsAvailable was not called.
	TxsAvailable() <-chan struct{}

	// EnableTxsAvailable initializes the TxsAvailable channel, ensuring it will
	// trigger once every height when transactions are available.
	EnableTxsAvailable()

	// Size returns the number of transactions in the mempool.
	Size() int

	// SizeBytes returns the total size of all txs in the mempool.
	SizeBytes() int64

	// Celestia Specific Methods

	// GetTxByKey gets a tx by its key from the mempool. Returns the tx and a bool indicating its presence in the tx cache.
	// Used in the RPC endpoint: TxStatus.
	GetTxByKey(key types.TxKey) (*types.CachedTx, bool)

	// WasRecentlyEvicted returns true if the tx was evicted from the mempool and exists in the
	// evicted cache.
	// Used in the RPC endpoint: TxStatus.
	WasRecentlyEvicted(key types.TxKey) bool

	// WasRecentlyRejected returns a bool indicating if the tx was rejected from the mempool and exists in the
	// rejected cache alongside the rejection code.
	// Used in the RPC endpoint: TxStatus.
	WasRecentlyRejected(key types.TxKey) (bool, uint32)
}

// PreCheckFunc is an optional filter executed before CheckTx and rejects
// transaction if false is returned. An example would be to ensure that a
// transaction doesn't exceeded the block size.
type PreCheckFunc func(*types.CachedTx) error

// PostCheckFunc is an optional filter executed after CheckTx and rejects
// transaction if false is returned. An example would be to ensure a
// transaction doesn't require more gas than available for the block.
type PostCheckFunc func(*types.CachedTx, *abci.ResponseCheckTx) error

// PreCheckMaxBytes checks that the size of the transaction is smaller or equal
// to the expected maxBytes.
func PreCheckMaxBytes(maxBytes int64) PreCheckFunc {
	return func(tx *types.CachedTx) error {
		txSize := types.ComputeProtoSizeForTxs([]types.Tx{tx.Tx})

		if txSize > maxBytes {
			return fmt.Errorf("tx size is too big: %d, max: %d", txSize, maxBytes)
		}

		return nil
	}
}

// PostCheckMaxGas checks that the wanted gas is smaller or equal to the passed
// maxGas. Returns nil if maxGas is -1.
func PostCheckMaxGas(maxGas int64) PostCheckFunc {
	return func(tx *types.CachedTx, res *abci.ResponseCheckTx) error {
		if maxGas == -1 {
			return nil
		}
		if res.GasWanted < 0 {
			return fmt.Errorf("gas wanted %d is negative",
				res.GasWanted)
		}
		if res.GasWanted > maxGas {
			return fmt.Errorf("gas wanted %d is greater than max gas %d",
				res.GasWanted, maxGas)
		}

		return nil
	}
}

// TxKey is the fixed length array key used as an index.
type TxKey [sha256.Size]byte
