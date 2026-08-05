package cat

import (
	"errors"
	"fmt"

	"github.com/cometbft/cometbft/internal/protowire"
	"github.com/cometbft/cometbft/mempool"
)

// Field numbers within tendermint.mempool.Message and tendermint.mempool.Txs.
// Both are 1: Txs is the first member of the Message oneof, and the repeated
// tx entries are the only field of Txs.
const (
	fieldMessageTxs = 1 // Message.sum: `Txs txs = 1`
	fieldTxsEntry   = 1 // Txs: `repeated bytes txs = 1`
)

var (
	// ErrEmptyTxEntry is returned when a Txs message carries an entry with no
	// payload. An empty entry costs almost nothing on the wire but still forces
	// an allocation when unmarshalled, which is the heap amplification vector
	// this filter exists to close.
	ErrEmptyTxEntry = errors.New("mempool message contains an empty transaction entry")
	// ErrTooManyTxEntries is returned when a Txs message carries more entries
	// than mempool.MaxTxsPerMessage.
	ErrTooManyTxEntries = fmt.Errorf("mempool message contains more than %d transaction entries", mempool.MaxTxsPerMessage)
	// ErrTxTooLarge is returned when a single transaction entry exceeds the
	// configured maximum transaction size.
	ErrTxTooLarge = errors.New("transaction exceeds max tx size")
)

// filterTxsMsgBytesFn returns a p2p RecvMessagePrecheck that rejects abusive
// Txs messages before they are unmarshalled.
func filterTxsMsgBytesFn(maxTxSize int) func([]byte) error {
	return func(msgBytes []byte) error {
		return filterTxsMsgBytes(msgBytes, maxTxSize)
	}
}

// filterTxsMsgBytes walks the raw wire bytes of a tendermint.mempool.Message
// and rejects Txs submessages that could not have come from an honest peer.
// Without this check, a small count-packed payload unmarshals into a
// disproportionately large in-memory structure.
//
// Messages that carry no Txs submessage (SeenTx, WantTx) pass through, as does
// a Txs submessage with no entries: it allocates nothing, so the reactor's own
// check owns that rejection and keeps the error attributed to the right place.
//
// Rules:
//
//  1. Well-formed: every varint, tag and length prefix must stay inside the
//     buffer. Truncated, overflowing or out-of-bounds encodings are rejected.
//  2. No empty entries: every transaction must carry at least one byte.
//  3. Per-tx bound: no entry may exceed maxTxSize. A non-positive maxTxSize
//     disables this check.
//  4. Entry count: no more than mempool.MaxTxsPerMessage entries, mirroring the
//     reactor's post-unmarshal check.
func filterTxsMsgBytes(msgBytes []byte, maxTxSize int) error {
	msg := protowire.NewWireCursor(msgBytes)
	for !msg.AtEnd() {
		fieldNum, wireType, err := msg.ReadTag()
		if err != nil {
			return err
		}

		// Anything that is not the Txs submessage is skipped, so unknown or
		// future fields do not break the scan.
		if fieldNum != fieldMessageTxs || wireType != protowire.WireBytes {
			if err := msg.SkipField(wireType); err != nil {
				return err
			}
			continue
		}

		txsBytes, err := msg.ReadLengthDelimited()
		if err != nil {
			return err
		}
		if err := scanTxsSubmessage(txsBytes, maxTxSize); err != nil {
			return err
		}
	}

	return nil
}

// scanTxsSubmessage walks a Txs submessage (`repeated bytes txs = 1`) and
// validates each entry against the limits.
func scanTxsSubmessage(txsBytes []byte, maxTxSize int) error {
	txs := protowire.NewWireCursor(txsBytes)
	count := 0
	for !txs.AtEnd() {
		fieldNum, wireType, err := txs.ReadTag()
		if err != nil {
			return err
		}

		if fieldNum != fieldTxsEntry || wireType != protowire.WireBytes {
			if err := txs.SkipField(wireType); err != nil {
				return err
			}
			continue
		}

		tx, err := txs.ReadLengthDelimited()
		if err != nil {
			return err
		}

		if len(tx) == 0 {
			return ErrEmptyTxEntry
		}
		if maxTxSize > 0 && len(tx) > maxTxSize {
			return fmt.Errorf("%w: %d > %d", ErrTxTooLarge, len(tx), maxTxSize)
		}

		count++
		if count > mempool.MaxTxsPerMessage {
			return ErrTooManyTxEntries
		}
	}

	return nil
}
