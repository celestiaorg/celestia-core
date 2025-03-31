package propagation

import (
	"math/rand"

	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/pkg/trace"
	protoprop "github.com/tendermint/tendermint/proto/tendermint/propagation"
	"github.com/tendermint/tendermint/types"
)

// retryWants ensure that all data for all unpruned compact blocks is requested.
//
// todo: add a request limit for each part to avoid downloading the block too
// many times. atm, this code will request the same part from every peer.
func (blockProp *Reactor) retryWants(currentHeight int64) {
	data := blockProp.unfinishedHeights()
	peers := blockProp.getPeers()
	for _, prop := range data {
		height, round := prop.compactBlock.Proposal.Height, prop.compactBlock.Proposal.Round

		// don't re-request parts for any round on the current height
		if height == currentHeight {
			continue
		}

		if prop.block.IsComplete() {
			continue
		}

		// only re-request original parts that are missing, not parity parts.
		missing := prop.block.MissingOriginal()
		if missing.IsEmpty() {
			blockProp.Logger.Error("no missing parts yet block is incomplete", "height", height, "round", round)
			continue
		}

		WriteRetries(blockProp.traceClient, height, round, missing.String())

		// make requests from different peers
		peers = shuffle(peers)

		for _, peer := range peers {
			mc := missing.Copy()

			reqs, has := peer.GetRequests(height, round)
			if has {
				mc = mc.Sub(reqs)
			}

			if mc.IsEmpty() {
				continue
			}

			e := p2p.Envelope{
				ChannelID: WantChannel,
				Message: &protoprop.WantParts{
					Parts:  *mc.ToProto(),
					Height: height,
					Round:  round,
					Prove:  true,
				},
			}

			if !p2p.TrySendEnvelopeShim(peer.peer, e, blockProp.Logger) { //nolint:staticcheck
				blockProp.Logger.Error("failed to send want part", "peer", peer, "height", height, "round", round)
				continue
			}

			WriteCatchupRequest(blockProp.traceClient, height, round, mc.String(), string(peer.peer.ID()))

			// keep track of which requests we've made this attempt.
			missing = missing.Sub(mc)
			peer.AddRequests(height, round, missing)
		}
	}
}

func (blockProp *Reactor) AddCommitment(height int64, round int32, psh *types.PartSetHeader) {
	blockProp.Logger.Info("adding commitment", "height", height, "round", round, "psh", psh)
	blockProp.pmtx.Lock()
	defer blockProp.pmtx.Unlock()

	blockProp.Logger.Info("added commitment", "height", height, "round", round)
	WriteGap(blockProp.traceClient, height, round)

	if blockProp.proposals[height] == nil {
		blockProp.proposals[height] = make(map[int32]*proposalData)
	}

	combinedSet := proptypes.NewCombinedPartSetFromOriginal(types.NewPartSetFromHeader(*psh), true)

	if blockProp.proposals[height][round] != nil {
		return
	}

	blockProp.proposals[height][round] = &proposalData{
		compactBlock: &proptypes.CompactBlock{
			Proposal: types.Proposal{
				Height: height,
				Round:  round,
			},
		},
		catchup:     true,
		block:       combinedSet,
		maxRequests: bits.NewBitArray(int(psh.Total * 2)), // this assumes that the parity parts are the same size
	}
}

func shuffle[T any](slice []T) []T {
	n := len(slice)
	for i := n - 1; i > 0; i-- {
		j := rand.Intn(i + 1)
		slice[i], slice[j] = slice[j], slice[i]
	}
	return slice
}

const (
	CatchupRequestsTable = "catch_reqs"
)

type CatchupRequest struct {
	Height int64  `json:"height"`
	Round  int32  `json:"round"`
	Parts  string `json:"parts"`
	Peer   string `json:"peer"`
}

func (b CatchupRequest) Table() string {
	return CatchupRequestsTable
}

func WriteCatchupRequest(
	client trace.Tracer,
	height int64,
	round int32,
	parts string,
	peer string,
) {
	// this check is redundant to what is checked during client.Write, although it
	// is an optimization to avoid allocations from the map of fields.
	if !client.IsCollecting(CatchupRequestsTable) {
		return
	}
	client.Write(CatchupRequest{
		Height: height,
		Round:  round,
		Parts:  parts,
		Peer:   peer,
	})
}

const (
	RetriesTable = "retries"
)

type Retries struct {
	Height  int64  `json:"height"`
	Round   int32  `json:"round"`
	Missing string `json:"missing"`
}

func (b Retries) Table() string {
	return RetriesTable
}

func WriteRetries(
	client trace.Tracer,
	height int64,
	round int32,
	missing string,
) {
	client.Write(Retries{
		Height:  height,
		Round:   round,
		Missing: missing,
	})
}

const (
	GapTable = "gap"
)

type Gap struct {
	Height int64 `json:"height"`
	Round  int32 `json:"round"`
}

func (b Gap) Table() string {
	return GapTable
}

func WriteGap(
	client trace.Tracer,
	height int64,
	round int32,
) {
	client.Write(Gap{
		Height: height,
		Round:  round,
	})
}
