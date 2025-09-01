package propagation

import (
	"errors"
	"fmt"
	"time"

	"github.com/cosmos/gogoproto/proto"

	proptypes "github.com/cometbft/cometbft/consensus/propagation/types"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/bits"
	"github.com/cometbft/cometbft/libs/trace/schema"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/proto/tendermint/mempool"
	"github.com/cometbft/cometbft/proto/tendermint/propagation"
	"github.com/cometbft/cometbft/types"
)

var _ Propagator = (*Reactor)(nil)

const (
	CompactBlockUID = "compactBlock"
)

// ProposeBlock is called when the consensus routine has created a new proposal,
// and it needs to be gossiped to the rest of the network.
func (blockProp *Reactor) ProposeBlock(proposal *types.Proposal, block *types.PartSet, txs []proptypes.TxMetaData) {
	// create the parity data and the compact block
	parityBlock, lastLen, err := types.Encode(block, types.BlockPartSizeBytes)
	if err != nil {
		blockProp.Logger.Error("failed to encode block", "err", err)
		return
	}

	fmt.Println("ProposeBlock.ExtractHashes: ", time.Now())
	partHashes := extractHashes(block, parityBlock)
	fmt.Println("ProposeBlock.ExtractProofs: ", time.Now())
	proofs := extractProofs(block, parityBlock)
	fmt.Println("ProposeBlock.DoneExtractProofs: ", time.Now())

	cb := proptypes.CompactBlock{
		Proposal:    *proposal,
		LastLen:     uint32(lastLen),
		BpHash:      parityBlock.Hash(),
		Blobs:       txs,
		PartsHashes: partHashes,
	}

	cb.SetProofCache(proofs)

	sbz, err := cb.SignBytes()
	if err != nil {
		blockProp.Logger.Error("failed to create signature for compact block", "err", err)
		return
	}

	// sign the hash of the compact block NOTE: p2p message sign bytes are
	// prepended with the chain id and UID
	sig, err := blockProp.privval.SignRawBytes(blockProp.chainID, CompactBlockUID, sbz)
	if err != nil {
		blockProp.Logger.Error("failed to sign compact block - this may indicate an incompatible KMS version that does not support compact block signing",
			"err", err,
			"height", proposal.Height,
			"round", proposal.Round,
			"chain_id", blockProp.chainID,
			"unique_id", CompactBlockUID,
		)
		return
	}

	cb.Signature = sig

	// save the compact block locally and broadcast it to the connected peers
	blockProp.handleCompactBlock(&cb, blockProp.self, true)

	_, parts, _, has := blockProp.getAllState(proposal.Height, proposal.Round, false)
	if !has {
		panic(fmt.Sprintf("failed to get all state for this node's proposal %d/%d", proposal.Height, proposal.Round))
	}

	parts.SetProposalData(block, parityBlock)

	// distribute equal portions of haves to each of the proposer's peers
	peers := blockProp.getPeers()
	chunks := chunkParts(parts.Parity().BitArray(), len(peers), 1)
	// chunks = Shuffle(chunks)
	for index, peer := range peers {
		partsMeta := chunkToPartMetaData(chunks[index], parts)
		e := p2p.Envelope{
			ChannelID: DataChannel,
			Message: &propagation.HaveParts{
				Height: proposal.Height,
				Round:  proposal.Round,
				Parts:  partsMeta,
			},
		}

		if !peer.peer.TrySend(e) {
			blockProp.Logger.Error("failed to send have part", "peer", peer, "height", proposal.Height, "round", proposal.Round)
			continue
		}

		peer.Initialize(cb.Proposal.Height, cb.Proposal.Round, int(parts.Total()))

		for _, part := range partsMeta {
			// since this node is proposing, it already has the data and
			// there's not a lot of reason to update this peer's have
			// state other than to be consistent atm.
			err := peer.SetHave(proposal.Height, proposal.Round, int(part.GetIndex()))
			if err != nil {
				blockProp.Logger.Debug("failed to set have part peer state", "peer", peer, "height", proposal.Height, "round", proposal.Round, "error", err)
				// skip saving the old routine's state if the state here
				// cannot also be saved
				continue
			}

			// this might not get set depending on when the consensus peer
			// state is getting updated. This will result in sending the
			// peer redundant parts :shrug:
			peer.consensusPeerState.SetHasProposalBlockPart(proposal.Height, proposal.Round, int(part.GetIndex()))
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
	for _, partIndex := range chunk.GetTrueIndices() {
		part := partSet.Parity().GetPart(partIndex)
		partMetaData = append(partMetaData, &propagation.PartMetaData{
			Index: uint32(partIndex),
			Hash:  part.Proof.LeafHash,
		})
	}
	return partMetaData
}

// handleCompactBlock adds a proposal to the data routine. This should be called any
// time a proposal is received from a peer or when a proposal is created. If the
// proposal is new, it will be stored and broadcast to the relevant peers.
func (blockProp *Reactor) handleCompactBlock(cb *proptypes.CompactBlock, peer p2p.ID, proposer bool) {
	fmt.Println("handleCompactBlock.ReceivedCompactBlock: ", time.Now())
	added := blockProp.AddProposal(cb)
	if !added {
		p := blockProp.getPeer(peer)
		if p == nil {
			return
		}
		p.consensusPeerState.SetHasProposal(&cb.Proposal)
		return
	} else if !proposer {
		p := blockProp.getPeer(peer)
		if p == nil {
			return
		}
		p.consensusPeerState.SetHasProposal(&cb.Proposal)
	}

	err := blockProp.validateCompactBlock(cb)
	if !proposer && err != nil {
		blockProp.DeleteRound(cb.Proposal.Height, cb.Proposal.Round)
		blockProp.Logger.Debug("failed to validate proposal. ignoring", "err", err, "height", cb.Proposal.Height, "round", cb.Proposal.Round)
		return
	}

	// generate (and cache) the proofs from the partset hashes in the compact block
	_, err = cb.Proofs()
	if err != nil {
		blockProp.DeleteRound(cb.Proposal.Height, cb.Proposal.Round)
		blockProp.Logger.Error("received invalid compact block", "err", err.Error())
		blockProp.Switch.StopPeerForError(blockProp.getPeer(peer).peer, err, blockProp.String())
		return
	}

	if !proposer {
		select {
		case <-blockProp.ctx.Done():
			return
		case blockProp.proposalChan <- cb.Proposal:
		}
		// check if we have any transactions that are in the compact block
		blockProp.recoverPartsFromMempool(cb)
	}

	blockProp.broadcastCompactBlock(cb, peer)
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

	parts, err := proptypes.TxsToParts(txsFound, cb.Proposal.BlockID.PartSetHeader.Total, types.BlockPartSizeBytes, cb.LastLen)
	if err != nil {
		blockProp.Logger.Error("invalid compact block", "err", err)
		return
	}

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

	recoveredCount := 0
	haves := proptypes.HaveParts{
		Height: cb.Proposal.Height,
		Round:  cb.Proposal.Round,
		Parts:  make([]proptypes.PartMetaData, 0),
	}
	for _, p := range parts {
		if partSet.HasPart(int(p.Index)) {
			continue
		}
		p.Proof = *proofs[p.Index]

		added, err := partSet.AddOriginalPart(p)
		if err != nil {
			blockProp.Logger.Error("failed to add locally recovered part", "err", err)
			continue
		}

		if !added {
			blockProp.Logger.Error("failed to add locally recovered part", "part", p.Index)
			continue
		}

		select {
		case <-blockProp.ctx.Done():
			return
		case blockProp.partChan <- types.PartInfo{
			Part:   p,
			Height: cb.Proposal.Height,
			Round:  cb.Proposal.Round,
		}:
		}

		recoveredCount++

		haves.Parts = append(haves.Parts, proptypes.PartMetaData{Index: p.Index, Hash: p.Proof.LeafHash})
	}

	schema.WriteMempoolRecoveredParts(blockProp.traceClient, cb.Proposal.Height, cb.Proposal.Round, recoveredCount)

	if len(haves.Parts) > 0 {
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

		if !peer.peer.TrySend(e) {
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

// validateCompactBlock stateful validation of the compact block.
func (blockProp *Reactor) validateCompactBlock(cb *proptypes.CompactBlock) error {
	blockProp.mtx.Lock()
	proposer := blockProp.currentProposer
	blockProp.mtx.Unlock()
	blockProp.pmtx.Lock()
	currentHeight := blockProp.currentHeight
	currentRound := blockProp.currentRound
	blockProp.pmtx.Unlock()
	if proposer == nil {
		return errors.New("nil proposer key")
	}
	// Does not apply
	if cb.Proposal.Height != currentHeight || cb.Proposal.Round != currentRound {
		return fmt.Errorf("proposal height %v round %v does not match state height %v round %v", cb.Proposal.Height, cb.Proposal.Round, currentHeight, currentRound)
	}

	if cb.LastLen > types.BlockPartSizeBytes {
		return fmt.Errorf("invalid last length %d > %d", cb.LastLen, types.BlockPartSizeBytes)
	}

	// Verify POLRound, which must be -1 or in range [0, proposal.Round).
	if cb.Proposal.POLRound < -1 ||
		(cb.Proposal.POLRound >= 0 && cb.Proposal.POLRound >= cb.Proposal.Round) {
		return fmt.Errorf("error invalid proposal POL round: %v %v", cb.Proposal.POLRound, cb.Proposal.Round)
	}

	p := cb.Proposal.ToProto()
	// Verify proposal signature
	if !proposer.VerifySignature(
		types.ProposalSignBytes(blockProp.chainID, p), cb.Proposal.Signature,
	) {
		return errors.New("error invalid proposal signature")
	}

	// Validate the proposed block size, derived from its PartSetHeader
	maxBytes := blockProp.BlockMaxBytes
	if maxBytes == -1 {
		maxBytes = int64(types.MaxBlockSizeBytes)
	}
	if int64(cb.Proposal.BlockID.PartSetHeader.Total) > (maxBytes-1)/int64(types.BlockPartSizeBytes)+1 {
		return errors.New("proposal block has too many parts")
	}

	// validate the compact block
	cbz, err := cb.SignBytes()
	if err != nil {
		return err
	}

	p2pBz, err := types.RawBytesMessageSignBytes(blockProp.chainID, CompactBlockUID, cbz)
	if err != nil {
		return err
	}

	if proposer.VerifySignature(p2pBz, cb.Signature) {
		return nil
	}

	return fmt.Errorf("invalid signature over the compact block: height %d round %d", cb.Proposal.Height, cb.Proposal.Round)
}
