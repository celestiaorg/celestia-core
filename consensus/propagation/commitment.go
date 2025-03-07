package propagation

import (
	"fmt"
	"github.com/tendermint/tendermint/proto/tendermint/propagation"
	"sort"

	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	"github.com/tendermint/tendermint/types"
)

var _ Propagator = (*Reactor)(nil)

// ProposeBlock is called when the consensus routine has created a new proposal
// and it needs to be gossiped to the rest of the network.
func (blockProp *Reactor) ProposeBlock(proposal *types.Proposal, block *types.PartSet, txs []proptypes.TxMetaData) {
	// create the parity data and the compact block
	parityBlock, lastLen, err := types.Encode(block, types.BlockPartSizeBytes)
	if err != nil {
		blockProp.Logger.Error("failed to encode block", "err", err)
		return
	}

	// create the compact block
	cb := proptypes.CompactBlock{
		Proposal:  *proposal,
		LastLen:   uint32(lastLen),
		Signature: cmtrand.Bytes(64), // todo: sign the proposal with a real signature
		BpHash:    parityBlock.Hash(),
		Blobs:     txs,
	}

	// save the compact block locally and broadcast it to the connected peers
	blockProp.handleCompactBlock(&cb, blockProp.self)

	// distribute equal portions of haves to each of the proposer's peers
	peers := blockProp.getPeers()
	chunks := chunkParts(parityBlock.BitArray(), len(peers), 1)
	for index, peer := range peers {
		e := p2p.Envelope{
			ChannelID: DataChannel,
			Message: &propagation.HaveParts{
				Height: proposal.Height,
				Round:  proposal.Round,
				Parts:  chunkToPartMetaData(chunks[index], parityBlock),
			},
		}
		// TODO maybe use a different logger
		if !p2p.SendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
			blockProp.Logger.Error("failed to send have part", "peer", peer, "height", proposal.Height, "round", proposal.Round, "part", index)
			// TODO maybe retry
			continue
		}
	}
}

func chunkToPartMetaData(chunk *bits.BitArray, partSet *types.PartSet) []*propagation.PartMetaData {
	partMetaData := make([]*propagation.PartMetaData, 0)
	// TODO rename indice to a correct name
	for _, indice := range chunk.GetTrueIndices() {
		part := partSet.GetPart(indice)
		partMetaData = append(partMetaData, &propagation.PartMetaData{ // TODO create the programmatic type and use the ToProto method
			Index: part.Index,
			Hash:  part.Proof.LeafHash, // TODO this seems like a duplicate field, do we need it?
			Proof: *part.Proof.ToProto(),
		})
	}
	return partMetaData
}

// handleCompactBlock adds a proposal to the data routine. This should be called any
// time a proposal is received from a peer or when a proposal is created. If the
// proposal is new, it will be stored and broadcast to the relevant peers.
func (blockProp *Reactor) handleCompactBlock(cb *proptypes.CompactBlock, peer p2p.ID) {
	if peer != blockProp.self && (cb.Proposal.Height > blockProp.currentHeight+1 || cb.Proposal.Round > blockProp.currentRound+1) {
		// catchup on missing heights/rounds by requesting the missing parts from all connected peers.
		go blockProp.requestMissingBlocks(cb.Proposal.Height, cb.Proposal.Round)
	}

	added, _, _ := blockProp.AddProposal(cb)

	// todo: add catchup logic here by checking for gaps

	if added {
		blockProp.broadcastCompactBlock(cb, peer)
	}

	// check if we have any transactions that are in the compact block

	// broadcast the haves for the parts we have??
}

func (blockProp *Reactor) compactBlockToParts(cb *proptypes.CompactBlock) []*types.Part {
	// find the compact block transactions that exist in our mempool
	txsFound := make([]txFound, 0)
	for _, txMetaData := range cb.Blobs {
		txKey, err := types.TxKeyFromBytes(txMetaData.Hash)
		if err != nil {
			blockProp.Logger.Error("failed to decode tx key", "err", err, "tx", txMetaData)
			// TODO maybe do something other than just continuing
			continue
		}
		tx, has := blockProp.mempool.GetTxByKey(txKey)
		if !has {
			// the transaction hash is not found in the mempool.
			continue
		}
		txsFound = append(txsFound, txFound{metaData: txMetaData, key: txKey, txBytes: tx})
	}
	if len(txsFound) == 0 {
		// no compact block transaction was found locally
		return nil
	}

	parts := TxsToParts(txsFound)
}

func TxsToParts(txsFound []txFound) []*types.Part {
	// sort the txs found by start index
	sort.Slice(txsFound, func(i, j int) bool {
		return txsFound[i].metaData.Start < txsFound[j].metaData.Start
	})

	// the cumulative bytes slice will contain the transaction bytes along with
	// any left bytes from contiguous previous transactions
	cumulativeBytes := make([]byte, 0)
	// the start index of where the cumulative bytes start
	cumulativeBytesStartIndex := -1
	for index := 0; index < len(txsFound); index++ {
		// the transaction we're parsing
		currentTx := txsFound[index]
		// the part index where the transaction starts
		currentPartStartIndex := currentTx.metaData.Start / types.BlockPartSizeBytes
		// the index of the part to the block bytes where the transaction ends
		currentPartEndIndex := (currentPartStartIndex + 1) * types.BlockPartSizeBytes

		if len(cumulativeBytes) == 0 {
			cumulativeBytesStartIndex = int(currentTx.metaData.Start)
		}
		// append the current transaction bytes to the cumulative bytes slice
		cumulativeBytes = append(cumulativeBytes, currentTx.txBytes...)

		if cumulativeBytesStartIndex > int(currentPartStartIndex) {
			// relative part end index
			relativePartEndIndex := int(currentPartEndIndex) - cumulativeBytesStartIndex
			// slice the cumulative bytes to start at exactly the part end index
			cumulativeBytes = cumulativeBytes[relativePartEndIndex:]
		}
		if int(currentPartStartIndex) >= cumulativeBytesStartIndex && int(currentPartEndIndex) <= cumulativeBytesStartIndex+len(cumulativeBytes) {
			// process it with index of part == currentPartStartIndex
			// set the cumulative bytes start index to be the start index of the next part
			relativePartStartIndex := int(currentPartStartIndex) - cumulativeBytesStartIndex
			// slice the cumulative bytes to start at exactly the part start index
			cumulativeBytes = cumulativeBytes[relativePartStartIndex:]
			// process the part
			// set the cumulative start index to the part end index
		} else if currentPartEndIndex <= currentTx.metaData.End { // end or end-1???
			// this means that this transaction (or this transaction
			// along with the cumulative bytes from the previous transactions)
			// spans across one or more parts.
			// we will decode as many parts we can and then leave the remaining
			// bytes for the next transaction.
			offset := uint32(0)
			for offset < currentTx.metaData.End-currentTx.metaData.Start {
				// take part as: [offset, offset+part_size-1)
				// decode it
				// add it to parts
				partBz := cumulativeBytes[:types.BlockPartSizeBytes]
				offset += types.BlockPartSizeBytes
				// slice this part off the cumulative bytes
				cumulativeBytes = cumulativeBytes[types.BlockPartSizeBytes:]
				// set cumulative start index
			}
			// if cumulative is empty, set start to -1 (not needed)
		}

		// check whether the next transaction is a contingent to the current one.
		if index+1 < len(txsFound) {
			nextTx := txsFound[index+1]
			if currentTx.metaData.End == nextTx.metaData.Start {
				// the next transaction corresponds to the continuation of this part

				// the part index where the transaction starts
				nextPartStart := nextTx.metaData.Start / types.BlockPartSizeBytes
				// the index of the part relative to the block bytes where the transaction starts.
				// explain that this next part is the start of the next transaction!!
				nextPartStartIndex := nextPartStart * types.BlockPartSizeBytes

				if nextTx.metaData.Start != nextPartStartIndex && currentTx.metaData.Start > nextPartStartIndex {
					// FIXME This doesn't always work
					// this means the next transaction continues after the current part,
					// which means the previous part can't be retrieved.
					// so, we reset the cumulative Bytes
					cumulativeBytes = cumulativeBytes[:0]
					cumulativeBytesStartIndex = -1
				}
				continue
			} else {
				// using an else is more explicit and easier to understand.
				cumulativeBytes = cumulativeBytes[:0]
				cumulativeBytesStartIndex = -1
			}
		}
	}
}

// txFound is an intermediary type that allows keeping the transaction metadata,
// its key and the actual tx bytes.
// This will be used to create the parts from the local txs.
type txFound struct {
	metaData proptypes.TxMetaData
	key      types.TxKey
	txBytes  []byte
}

// broadcastProposal gossips the provided proposal to all peers. This should
// only be called upon receiving a proposal for the first time or after creating
// a proposal block.
func (blockProp *Reactor) broadcastCompactBlock(cb *proptypes.CompactBlock, from p2p.ID) {
	e := p2p.Envelope{
		ChannelID: DataChannel,
		Message:   cb.ToProto(),
	}

	peers := blockProp.getPeers()

	for _, peer := range peers {
		if peer.peer.ID() == from {
			continue
		}

		if !p2p.TrySendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
			blockProp.Logger.Debug("failed to send proposal to peer", "peer", peer.peer.ID())
			// todo: we need to avoid sending this peer anything else until we can queue this message.
			continue
		}
		schema.WriteProposal(blockProp.traceClient, cb.Proposal.Height, cb.Proposal.Round, string(peer.peer.ID()), schema.Upload)
	}
}

// chunkParts takes a bit array then returns an array of chunked bit arrays.
// TODO document how the redundancy and the peer count are used here.
func chunkParts(p *bits.BitArray, peerCount, redundancy int) []*bits.BitArray {
	size := p.Size()
	if peerCount == 0 {
		peerCount = 1
	}
	chunkSize := size / peerCount
	// round up to use the ceil
	if size%peerCount != 0 || chunkSize == 0 {
		chunkSize++
	}

	// Create empty bit arrays for each peer
	parts := make([]*bits.BitArray, peerCount)
	for i := 0; i < peerCount; i++ {
		parts[i] = bits.NewBitArray(size)
	}

	chunks := chunkIndexes(size, chunkSize)
	cursor := 0
	for p := 0; p < peerCount; p++ {
		for r := 0; r < redundancy; r++ {
			start, end := chunks[cursor][0], chunks[cursor][1]
			for i := start; i < end; i++ {
				parts[p].SetIndex(i, true)
			}
			cursor++
			if cursor >= len(chunks) {
				cursor = 0
			}
		}
	}

	return parts
}

// chunkIndexes
// TODO document and explain the parameters
func chunkIndexes(totalSize, chunkSize int) [][2]int {
	if totalSize <= 0 || chunkSize <= 0 {
		panic(fmt.Sprintf("invalid input: totalSize=%d, chunkSize=%d \n", totalSize, chunkSize))
		// return nil // Handle invalid input gracefully
	}

	var chunks [][2]int
	for start := 0; start < totalSize; start += chunkSize {
		end := start + chunkSize
		if end > totalSize {
			end = totalSize // Ensure the last chunk doesn't exceed the total size
		}
		chunks = append(chunks, [2]int{start, end})
	}

	return chunks
}
