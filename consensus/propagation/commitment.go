package propagation

import (
	"fmt"

	"github.com/tendermint/tendermint/crypto/merkle"

	"github.com/gogo/protobuf/proto"
	"github.com/tendermint/tendermint/proto/tendermint/mempool"
	"github.com/tendermint/tendermint/proto/tendermint/propagation"

	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	cmtrand "github.com/tendermint/tendermint/libs/rand"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace/schema"
	"github.com/tendermint/tendermint/types"
)

var _ Propagator = (*Reactor)(nil)

// ProposeBlock is called when the consensus routine has created a new proposal,
// and it needs to be gossiped to the rest of the network.
func (blockProp *Reactor) ProposeBlock(proposal *types.Proposal, block *types.PartSet, txs []proptypes.TxMetaData) {
	// create the parity data and the compact block
	parityBlock, lastLen, err := types.Encode(block, types.BlockPartSizeBytes)
	if err != nil {
		blockProp.Logger.Error("failed to encode block", "err", err)
		return
	}

	partHashes := extractHashes(block, parityBlock)
	proofs := extractProofs(block, parityBlock)

	cb := proptypes.CompactBlock{
		Proposal:    *proposal,
		LastLen:     uint32(lastLen),
		Signature:   cmtrand.Bytes(64), // todo: sign the proposal with a real signature
		BpHash:      parityBlock.Hash(),
		Blobs:       txs,
		PartsHashes: partHashes,
	}

	cb.SetProofCache(proofs)

	// save the compact block locally and broadcast it to the connected peers
	blockProp.handleCompactBlock(&cb, blockProp.self, true)

	_, parts, _, has := blockProp.getAllState(proposal.Height, proposal.Round, false)
	if !has {
		panic(fmt.Sprintf("failed to get all state for this node's proposal %d/%d", proposal.Height, proposal.Round))
	}

	parts.SetProposalData(block, parityBlock)

	// distribute equal portions of haves to each of the proposer's peers
	peers := blockProp.getPeers()
	chunks := chunkParts(parts.BitArray(), len(peers), 1)
	// chunks = Shuffle(chunks)
	for index, peer := range peers {
		e := p2p.Envelope{
			ChannelID: DataChannel,
			Message: &propagation.HaveParts{
				Height: proposal.Height,
				Round:  proposal.Round,
				Parts:  chunkToPartMetaData(chunks[index], parts),
			},
		}

		if !p2p.TrySendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
			blockProp.Logger.Error("failed to send have part", "peer", peer, "height", proposal.Height, "round", proposal.Round, "part", index)
			// TODO retry
			continue
		}

		schema.WriteBlockPartState(blockProp.traceClient, proposal.Height, proposal.Round, chunks[index].GetTrueIndices(), true, string(peer.peer.ID()), schema.Upload)
	}
}

func extractHashes(blocks ...*types.PartSet) [][]byte {
	total := uint32(0)
	for _, block := range blocks {
		total += block.Total()
	}

	partHashes := make([][]byte, 0, total) // Preallocate capacity
	for _, block := range blocks {
		for i := uint32(0); i < block.Total(); i++ {
			partHashes = append(partHashes, block.GetPart(int(i)).Proof.LeafHash)
		}
	}
	return partHashes
}

func extractProofs(blocks ...*types.PartSet) []*merkle.Proof {
	total := uint32(0)
	for _, block := range blocks {
		total += block.Total()
	}

	proofs := make([]*merkle.Proof, 0, total) // Preallocate capacity
	for _, block := range blocks {
		for i := uint32(0); i < block.Total(); i++ {
			proofs = append(proofs, &block.GetPart(int(i)).Proof)
		}
	}
	return proofs
}

func chunkToPartMetaData(chunk *bits.BitArray, partSet *proptypes.CombinedPartSet) []*propagation.PartMetaData {
	partMetaData := make([]*propagation.PartMetaData, 0)
	// TODO rename indice to a correct name
	for _, indice := range chunk.GetTrueIndices() {
		part, _ := partSet.GetPart(uint32(indice))
		partMetaData = append(partMetaData, &propagation.PartMetaData{
			Index: uint32(indice),
			Hash:  part.Proof.LeafHash,
		})
	}
	return partMetaData
}

// handleCompactBlock adds a proposal to the data routine. This should be called any
// time a proposal is received from a peer or when a proposal is created. If the
// proposal is new, it will be stored and broadcast to the relevant peers.
func (blockProp *Reactor) handleCompactBlock(cb *proptypes.CompactBlock, peer p2p.ID, proposer bool) {
	added := blockProp.AddProposal(cb)
	if !added {
		return
	}

	// generate (and cache) the proofs from the partset hashes in the compact block
	_, err := cb.Proofs()
	if err != nil {
		blockProp.DeleteRound(cb.Proposal.Height, cb.Proposal.Round)
		blockProp.Logger.Error("received invalid compact block", "err", err.Error())
		// todo: kick peer
		return
	}

	blockProp.broadcastCompactBlock(cb, peer)

	if proposer {
		return
	}

	// check if we have any transactions that are in the compact block
	go blockProp.recoverPartsFromMempool(cb)
}

// recoverPartsFromMempool queries the mempool to see if we can recover any block parts locally.
func (blockProp *Reactor) recoverPartsFromMempool(cb *proptypes.CompactBlock) {
	// find the compact block transactions that exist in our mempool
	txsFound := make([]proptypes.UnmarshalledTx, 0)
	for _, txMetaData := range cb.Blobs {
		txKey, err := types.TxKeyFromBytes(txMetaData.Hash)
		if err != nil {
			blockProp.Logger.Error("failed to decode tx key", "err", err, "tx", txMetaData)
			continue
		}

		tx, has := blockProp.mempool.GetTxByKey(txKey)
		if !has {
			continue
		}

		protoTxs := mempool.Txs{Txs: [][]byte{tx.Tx}}
		marshalledTx, err := proto.Marshal(&protoTxs)
		if err != nil {
			blockProp.Logger.Error("failed to encode tx", "err", err, "tx", txMetaData)
			continue
		}

		txsFound = append(txsFound, proptypes.UnmarshalledTx{MetaData: txMetaData, Key: txKey, TxBytes: marshalledTx})
	}

	if len(txsFound) == 0 {
		return
	}

	parts := proptypes.TxsToParts(txsFound)

	_, partSet, _, found := blockProp.getAllState(cb.Proposal.Height, cb.Proposal.Round, false)
	if !found {
		blockProp.Logger.Error("failed to get all state for this node's proposal", "height", cb.Proposal.Height, "round", cb.Proposal.Round)
	}

	proofs, err := cb.Proofs()
	if err != nil {
		blockProp.Logger.Error("failed to get proofs from compact block", "err", err)
		return
	}

	// todo: investigate why this could get hit, it shouldn't ever get hit
	if partSet == nil {
		blockProp.Logger.Error("unexpected nil partset while attempting to reuse transactions from the mempool")
		return
	}

	originalParts := partSet.Original()
	recoveredCount := 0
	haves := proptypes.HaveParts{
		Height: cb.Proposal.Height,
		Round:  cb.Proposal.Round,
		Parts:  make([]proptypes.PartMetaData, 0),
	}
	for _, p := range parts {
		p.Proof = *proofs[p.Index]

		// adding the part without checking the proof since the proof was just
		// created above.
		added, err := originalParts.AddPartWithoutProof(p)
		if err != nil {
			blockProp.Logger.Error("failed to add locally recovered part", "err", err)
			continue
		}

		if !added {
			blockProp.Logger.Error("failed to add locally recovered part", "part", p.Index)
			continue
		}

		recoveredCount++

		haves.Parts = append(haves.Parts, proptypes.PartMetaData{Index: p.Index, Hash: p.Proof.LeafHash})
	}

	schema.WriteMempoolRecoveredParts(blockProp.traceClient, cb.Proposal.Height, cb.Proposal.Round, recoveredCount)

	if len(haves.Parts) > 0 {
		// todo: distribute haves amongst peers in a fan out fashion similar to
		// a proposer
		blockProp.broadcastHaves(&haves, blockProp.self, int(partSet.Total()))
	}
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

// chunkIndexes creates a nested slice of starting and ending indexes for each
// chunk. totalSize indicates the number of chunks. chunkSize indicates the size
// of each chunk.
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
