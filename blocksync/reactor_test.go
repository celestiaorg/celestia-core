package blocksync

import (
	"crypto/rand"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	bcproto "github.com/cometbft/cometbft/proto/tendermint/blocksync"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	cfg "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/tmhash"
	"github.com/cometbft/cometbft/internal/test"
	"github.com/cometbft/cometbft/libs/log"
	mpmocks "github.com/cometbft/cometbft/mempool/mocks"
	"github.com/cometbft/cometbft/p2p"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	"github.com/cometbft/cometbft/types"
	cmttime "github.com/cometbft/cometbft/types/time"
)

var config *cfg.Config

func randGenesisDoc(numValidators int, randPower bool, minPower int64) (*types.GenesisDoc, []types.PrivValidator) {
	validators := make([]types.GenesisValidator, numValidators)
	privValidators := make([]types.PrivValidator, numValidators)
	for i := 0; i < numValidators; i++ {
		val, privVal := types.RandValidator(randPower, minPower)
		validators[i] = types.GenesisValidator{
			PubKey: val.PubKey,
			Power:  val.VotingPower,
		}
		privValidators[i] = privVal
	}
	sort.Sort(types.PrivValidatorsByAddress(privValidators))

	consPar := types.DefaultConsensusParams()
	consPar.ABCI.VoteExtensionsEnableHeight = 1
	return &types.GenesisDoc{
		GenesisTime:     cmttime.Now(),
		ChainID:         test.DefaultTestChainID,
		Validators:      validators,
		ConsensusParams: consPar,
	}, privValidators
}

type ReactorPair struct {
	reactor *ByzantineReactor
	app     proxy.AppConns
}

func newReactor(
	t *testing.T,
	logger log.Logger,
	genDoc *types.GenesisDoc,
	privVals []types.PrivValidator,
	maxBlockHeight int64,
	incorrectData ...int64,
) ReactorPair {
	return newReactorWithConfig(t, logger, genDoc, privVals, maxBlockHeight, true, incorrectData...)
}

func newReactorWithConfig(
	t *testing.T,
	logger log.Logger,
	genDoc *types.GenesisDoc,
	privVals []types.PrivValidator,
	maxBlockHeight int64,
	verifyData bool,
	incorrectData ...int64,
) ReactorPair {
	if len(privVals) != 1 {
		panic("only support one validator")
	}
	var incorrectBlock int64 = 0
	if len(incorrectData) > 0 {
		incorrectBlock = incorrectData[0]
	}

	app := abci.NewBaseApplication()
	cc := proxy.NewLocalClientCreator(app)
	proxyApp := proxy.NewAppConns(cc, proxy.NopMetrics())
	err := proxyApp.Start()
	if err != nil {
		panic(fmt.Errorf("error start app: %w", err))
	}

	blockDB := dbm.NewMemDB()
	stateDB := dbm.NewMemDB()
	stateStore := sm.NewStore(stateDB, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockStore := store.NewBlockStore(blockDB)

	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	if err != nil {
		panic(fmt.Errorf("error constructing state from genesis file: %w", err))
	}

	mp := &mpmocks.Mempool{}
	mp.On("Lock").Return()
	mp.On("Unlock").Return()
	mp.On("FlushAppConn", mock.Anything).Return(nil)
	mp.On("Update",
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything,
		mock.Anything).Return(nil)

	// Make the Reactor itself.
	// NOTE we have to create and commit the blocks first because
	// pool.height is determined from the store.
	blockSync := true
	db := dbm.NewMemDB()
	stateStore = sm.NewStore(db, sm.StoreOptions{
		DiscardABCIResponses: false,
	})
	blockExec := sm.NewBlockExecutor(stateStore, log.TestingLogger(), proxyApp.Consensus(),
		mp, sm.EmptyEvidencePool{}, blockStore)
	if err = stateStore.Save(state); err != nil {
		panic(err)
	}

	// The commit we are building for the current height.
	seenExtCommit := &types.ExtendedCommit{}

	pubKey, err := privVals[0].GetPubKey()
	if err != nil {
		panic(err)
	}
	addr := pubKey.Address()
	idx, _ := state.Validators.GetByAddress(addr)

	// let's add some blocks in
	for blockHeight := int64(1); blockHeight <= maxBlockHeight; blockHeight++ {
		voteExtensionIsEnabled := genDoc.ConsensusParams.ABCI.VoteExtensionsEnabled(blockHeight)

		lastExtCommit := seenExtCommit.Clone()

		thisBlock, thisParts, err := state.MakeBlock(blockHeight, types.MakeData([]types.Tx{}), lastExtCommit.ToCommit(), nil, state.Validators.Proposer.Address)
		require.NoError(t, err)

		blockID := types.BlockID{Hash: thisBlock.Hash(), PartSetHeader: thisParts.Header()}

		// Simulate a commit for the current height
		vote, err := types.MakeVote(
			privVals[0],
			thisBlock.Header.ChainID, //nolint:staticcheck
			idx,
			thisBlock.Header.Height, //nolint:staticcheck
			0,
			cmtproto.PrecommitType,
			blockID,
			time.Now(),
		)
		if err != nil {
			panic(err)
		}
		seenExtCommit = &types.ExtendedCommit{
			Height:             vote.Height,
			Round:              vote.Round,
			BlockID:            blockID,
			ExtendedSignatures: []types.ExtendedCommitSig{vote.ExtendedCommitSig()},
		}

		state, err = blockExec.ApplyBlock(state, blockID, thisBlock, lastExtCommit.ToCommit())
		if err != nil {
			panic(fmt.Errorf("error apply block: %w", err))
		}

		saveCorrectVoteExtensions := blockHeight != incorrectBlock
		if saveCorrectVoteExtensions == voteExtensionIsEnabled {
			blockStore.SaveBlockWithExtendedCommit(thisBlock, thisParts, seenExtCommit)
		} else {
			blockStore.SaveBlock(thisBlock, thisParts, seenExtCommit.ToCommit())
		}
	}

	bcReactor := NewByzantineReactor(incorrectBlock, NewReactor(state.Copy(), blockExec, blockStore, blockSync, NopMetrics(), 0, ReactorVerifyData(verifyData)))
	bcReactor.SetLogger(logger.With("module", "blocksync"))

	return ReactorPair{bcReactor, proxyApp}
}

func TestVerifyData(t *testing.T) {
	config = test.ResetTestRoot("blocksync_verify_data_test")
	defer os.RemoveAll(config.RootDir)
	genDoc, privVals := randGenesisDoc(1, false, 30)

	maxBlockHeight := int64(10)

	// Case 1: verifyData = true. Normal behavior.
	// We cannot easily mock ProcessProposal failure here because we are using a real app (NewBaseApplication) via proxy.
	// But we can observe that it works.
	// To test failure, we would need to mock the app or the proxy connection.
	// The current newReactor setup uses a real BaseApplication.
	// So, passing verifyData=false should also work for normal blocks.
	// To really test this, we might need a way to make ProcessProposal fail.

	// Let's verify that we can construct reactors with both settings.
	rp1 := newReactorWithConfig(t, log.TestingLogger(), genDoc, privVals, maxBlockHeight, true)
	rp2 := newReactorWithConfig(t, log.TestingLogger(), genDoc, privVals, maxBlockHeight, false)

	require.NotNil(t, rp1.reactor)
	require.NotNil(t, rp2.reactor)

	// We can check the internal field
	assert.True(t, rp1.reactor.verifyData)
	assert.False(t, rp2.reactor.verifyData)
}

func TestNoBlockResponse(t *testing.T) {
	config = test.ResetTestRoot("blocksync_reactor_test")
	defer os.RemoveAll(config.RootDir)
	genDoc, privVals := randGenesisDoc(1, false, 30)

	maxBlockHeight := int64(65)

	reactorPairs := make([]ReactorPair, 2)

	reactorPairs[0] = newReactor(t, log.TestingLogger(), genDoc, privVals, maxBlockHeight)
	reactorPairs[1] = newReactor(t, log.TestingLogger(), genDoc, privVals, 0)

	p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("BLOCKSYNC", reactorPairs[i].reactor)
		return s
	}, p2p.Connect2Switches)

	defer func() {
		for _, r := range reactorPairs {
			err := r.reactor.Stop()
			require.NoError(t, err)
			err = r.app.Stop()
			require.NoError(t, err)
		}
	}()

	tests := []struct {
		height   int64
		existent bool
	}{
		{maxBlockHeight + 2, false},
		{10, true},
		{1, true},
		{100, false},
	}

	for {
		if reactorPairs[1].reactor.pool.IsCaughtUp() { //nolint:staticcheck
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	assert.Equal(t, maxBlockHeight, reactorPairs[0].reactor.store.Height())

	for _, tt := range tests {
		block := reactorPairs[1].reactor.store.LoadBlock(tt.height)
		if tt.existent {
			assert.True(t, block != nil)
		} else {
			assert.True(t, block == nil)
		}
	}
}

// NOTE: This is too hard to test without
// an easy way to add test peer to switch
// or without significant refactoring of the module.
// Alternatively we could actually dial a TCP conn but
// that seems extreme.
func TestBadBlockStopsPeer(t *testing.T) {
	config = test.ResetTestRoot("blocksync_reactor_test")
	defer os.RemoveAll(config.RootDir)
	genDoc, privVals := randGenesisDoc(1, false, 30)

	maxBlockHeight := int64(148)

	// Other chain needs a different validator set
	otherGenDoc, otherPrivVals := randGenesisDoc(1, false, 30)
	otherChain := newReactor(t, log.TestingLogger(), otherGenDoc, otherPrivVals, maxBlockHeight)

	defer func() {
		err := otherChain.reactor.Stop()
		require.Error(t, err)
		err = otherChain.app.Stop()
		require.NoError(t, err)
	}()

	reactorPairs := make([]ReactorPair, 4) //nolint:prealloc

	reactorPairs[0] = newReactor(t, log.TestingLogger(), genDoc, privVals, maxBlockHeight)
	reactorPairs[1] = newReactor(t, log.TestingLogger(), genDoc, privVals, 0)
	reactorPairs[2] = newReactor(t, log.TestingLogger(), genDoc, privVals, 0)
	reactorPairs[3] = newReactor(t, log.TestingLogger(), genDoc, privVals, 0)

	switches := p2p.MakeConnectedSwitches(config.P2P, 4, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("BLOCKSYNC", reactorPairs[i].reactor)
		return s
	}, p2p.Connect2Switches)

	defer func() {
		for _, r := range reactorPairs {
			err := r.reactor.Stop()
			require.NoError(t, err)

			err = r.app.Stop()
			require.NoError(t, err)
		}
	}()

	for {
		time.Sleep(1 * time.Second)
		caughtUp := true
		for _, r := range reactorPairs {
			if !r.reactor.pool.IsCaughtUp() {
				caughtUp = false
			}
		}
		if caughtUp {
			break
		}
	}

	// at this time, reactors[0-3] is the newest
	assert.Equal(t, 3, reactorPairs[1].reactor.Switch.Peers().Size())

	// Mark reactorPairs[3] as an invalid peer. Fiddling with .store without a mutex is a data
	// race, but can't be easily avoided.
	reactorPairs[3].reactor.store = otherChain.reactor.store

	lastReactorPair := newReactor(t, log.TestingLogger(), genDoc, privVals, 0)
	reactorPairs = append(reactorPairs, lastReactorPair)

	switches = append(switches, p2p.MakeConnectedSwitches(config.P2P, 1, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("BLOCKSYNC", reactorPairs[len(reactorPairs)-1].reactor)
		return s
	}, p2p.Connect2Switches)...)

	for i := 0; i < len(reactorPairs)-1; i++ {
		p2p.Connect2Switches(switches, i, len(reactorPairs)-1)
	}

	for {
		if lastReactorPair.reactor.pool.IsCaughtUp() || lastReactorPair.reactor.Switch.Peers().Size() == 0 { //nolint:staticcheck
			break
		}

		time.Sleep(1 * time.Second)
	}

	assert.True(t, lastReactorPair.reactor.Switch.Peers().Size() < len(reactorPairs)-1)
}

func TestCheckSwitchToConsensusLastHeightZero(t *testing.T) {
	const maxBlockHeight = int64(45)

	config = test.ResetTestRoot("blocksync_reactor_test")
	defer os.RemoveAll(config.RootDir)
	genDoc, privVals := randGenesisDoc(1, false, 30)

	reactorPairs := make([]ReactorPair, 1, 2)
	reactorPairs[0] = newReactor(t, log.TestingLogger(), genDoc, privVals, 0)
	reactorPairs[0].reactor.switchToConsensusMs = 50
	defer func() {
		for _, r := range reactorPairs {
			err := r.reactor.Stop()
			require.NoError(t, err)
			err = r.app.Stop()
			require.NoError(t, err)
		}
	}()

	reactorPairs = append(reactorPairs, newReactor(t, log.TestingLogger(), genDoc, privVals, maxBlockHeight))

	var switches []*p2p.Switch //nolint:prealloc
	for _, r := range reactorPairs {
		switches = append(switches, p2p.MakeConnectedSwitches(config.P2P, 1, func(i int, s *p2p.Switch) *p2p.Switch {
			s.AddReactor("BLOCKSYNC", r.reactor)
			return s
		}, p2p.Connect2Switches)...)
	}

	time.Sleep(60 * time.Millisecond)

	// Connect both switches
	p2p.Connect2Switches(switches, 0, 1)

	startTime := time.Now()
	for {
		time.Sleep(20 * time.Millisecond)
		caughtUp := true
		for _, r := range reactorPairs {
			if !r.reactor.pool.IsCaughtUp() {
				caughtUp = false
				break
			}
		}
		if caughtUp {
			break
		}
		if time.Since(startTime) > 90*time.Second {
			msg := "timeout: reactors didn't catch up;"
			for i, r := range reactorPairs {
				h, p, lr := r.reactor.pool.GetStatus()
				c := r.reactor.pool.IsCaughtUp()
				msg += fmt.Sprintf(" reactor#%d (h %d, p %d, lr %d, c %t);", i, h, p, lr, c)
			}
			require.Fail(t, msg)
		}
	}

	// -1 because of "-1" in IsCaughtUp
	// -1 pool.height points to the _next_ height
	// -1 because we measure height of block store
	const maxDiff = 3
	for _, r := range reactorPairs {
		assert.GreaterOrEqual(t, r.reactor.store.Height(), maxBlockHeight-maxDiff)
	}
}

func ExtendedCommitNetworkHelper(t *testing.T, maxBlockHeight int64, enableVoteExtensionAt int64, invalidBlockHeightAt int64) {
	config = test.ResetTestRoot("blocksync_reactor_test")
	defer os.RemoveAll(config.RootDir)
	genDoc, privVals := randGenesisDoc(1, false, 30)
	genDoc.ConsensusParams.ABCI.VoteExtensionsEnableHeight = enableVoteExtensionAt

	reactorPairs := make([]ReactorPair, 1, 2)
	reactorPairs[0] = newReactor(t, log.TestingLogger(), genDoc, privVals, 0)
	reactorPairs[0].reactor.switchToConsensusMs = 50
	defer func() {
		for _, r := range reactorPairs {
			err := r.reactor.Stop()
			require.NoError(t, err)
			err = r.app.Stop()
			require.NoError(t, err)
		}
	}()

	reactorPairs = append(reactorPairs, newReactor(t, log.TestingLogger(), genDoc, privVals, maxBlockHeight, invalidBlockHeightAt))

	var switches []*p2p.Switch //nolint:prealloc
	for _, r := range reactorPairs {
		switches = append(switches, p2p.MakeConnectedSwitches(config.P2P, 1, func(i int, s *p2p.Switch) *p2p.Switch {
			s.AddReactor("BLOCKSYNC", r.reactor)
			return s
		}, p2p.Connect2Switches)...)
	}

	time.Sleep(60 * time.Millisecond)

	// Connect both switches
	p2p.Connect2Switches(switches, 0, 1)

	startTime := time.Now()
	for {
		time.Sleep(20 * time.Millisecond)
		// The reactor can never catch up, because at one point it disconnects.
		require.False(t, reactorPairs[0].reactor.pool.IsCaughtUp(), "node caught up when it should not have")
		// After 5 seconds, the test should have executed.
		if time.Since(startTime) > 5*time.Second {
			assert.Equal(t, 0, reactorPairs[0].reactor.Switch.Peers().Size(), "node should have disconnected but didn't")
			assert.Equal(t, 0, reactorPairs[1].reactor.Switch.Peers().Size(), "node should have disconnected but didn't")
			break
		}
	}
}

// TestCheckExtendedCommitExtra tests when VoteExtension is disabled but an ExtendedVote is present in the block.
func TestCheckExtendedCommitExtra(t *testing.T) {
	const maxBlockHeight = 10
	const enableVoteExtension = 5
	const invalidBlockHeight = 3

	ExtendedCommitNetworkHelper(t, maxBlockHeight, enableVoteExtension, invalidBlockHeight)
}

// TestCheckExtendedCommitMissing tests when VoteExtension is enabled but the ExtendedVote is missing from the block.
func TestCheckExtendedCommitMissing(t *testing.T) {
	const maxBlockHeight = 10
	const enableVoteExtension = 5
	const invalidBlockHeight = 8

	ExtendedCommitNetworkHelper(t, maxBlockHeight, enableVoteExtension, invalidBlockHeight)
}

// ByzantineReactor is a blockstore reactor implementation where a corrupted block can be sent to a peer.
// The corruption is that the block contains extended commit signatures when vote extensions are disabled or
// it has no extended commit signatures while vote extensions are enabled.
// If the corrupted block height is set to 0, the reactor behaves as normal.
type ByzantineReactor struct {
	*Reactor
	corruptedBlock int64
	// invalidBlock, when set, is served in place of the stored block at its
	// height. Its LastCommitHash is consistent (so it passes receipt
	// validation) but its LastCommit has one invalid signature past the +2/3
	// threshold, which is caught only by commit verification in poolRoutine.
	invalidBlock *types.Block
}

func NewByzantineReactor(invalidBlock int64, conR *Reactor) *ByzantineReactor {
	r := &ByzantineReactor{
		Reactor:        conR,
		corruptedBlock: invalidBlock,
	}
	r.BaseReactor = *p2p.NewBaseReactor("ByzantineBlockSync", r, p2p.WithIncomingQueueSize(10))
	return r
}

// respondToPeer (overridden method) loads a block and sends it to the requesting peer,
// if we have it. Otherwise, we'll respond saying we don't have it.
// Byzantine modification: if corruptedBlock is set, send the wrong Block.
func (bcR *ByzantineReactor) respondToPeer(msg *bcproto.BlockRequest, src p2p.Peer) (queued bool) {
	block := bcR.store.LoadBlock(msg.Height)
	// Byzantine modification: serve a pre-built invalid block in place of the
	// honest one at this height (see ByzantineReactor.invalidBlock).
	if bcR.invalidBlock != nil && msg.Height == bcR.invalidBlock.Height {
		block = bcR.invalidBlock
	}
	if block == nil {
		bcR.Logger.Info("Peer asking for a block we don't have", "src", src, "height", msg.Height)
		return src.TrySend(p2p.Envelope{
			ChannelID: BlocksyncChannel,
			Message:   &bcproto.NoBlockResponse{Height: msg.Height},
		})
	}

	state, err := bcR.blockExec.Store().Load()
	if err != nil {
		bcR.Logger.Error("loading state", "err", err)
		return false
	}
	var extCommit *types.ExtendedCommit
	voteExtensionEnabled := state.ConsensusParams.ABCI.VoteExtensionsEnabled(msg.Height)
	incorrectBlock := bcR.corruptedBlock == msg.Height
	if voteExtensionEnabled && !incorrectBlock || !voteExtensionEnabled && incorrectBlock {
		extCommit = bcR.store.LoadBlockExtendedCommit(msg.Height)
		if extCommit == nil {
			bcR.Logger.Error("found block in store with no extended commit", "block", block)
			return false
		}
	}

	bl, err := block.ToProto()
	if err != nil {
		bcR.Logger.Error("could not convert msg to protobuf", "err", err)
		return false
	}

	return src.TrySend(p2p.Envelope{
		ChannelID: BlocksyncChannel,
		Message: &bcproto.BlockResponse{
			Block:     bl,
			ExtCommit: extCommit.ToProto(),
		},
	})
}

// Receive implements Reactor by handling 4 types of messages (look below).
// Copied unchanged from reactor.go so the correct respondToPeer is called.
func (bcR *ByzantineReactor) Receive(e p2p.Envelope) {
	fmt.Println("Receive", e.Message)
	if err := ValidateMsg(e.Message); err != nil {
		bcR.Logger.Error("Peer sent us invalid msg", "peer", e.Src, "msg", e.Message, "err", err)
		bcR.Switch.StopPeerForError(e.Src, err, bcR.String())
		return
	}

	bcR.Logger.Debug("Receive", "e.Src", e.Src, "chID", e.ChannelID, "msg", e.Message)

	switch msg := e.Message.(type) {
	case *bcproto.BlockRequest:
		bcR.respondToPeer(msg, e.Src)
	case *bcproto.BlockResponse:
		bi, err := types.BlockFromProto(msg.Block)
		if err != nil {
			bcR.Logger.Error("Peer sent us invalid block", "peer", e.Src, "msg", e.Message, "err", err)
			bcR.Switch.StopPeerForError(e.Src, err, bcR.String())
			return
		}
		var extCommit *types.ExtendedCommit
		if msg.ExtCommit != nil {
			var err error
			extCommit, err = types.ExtendedCommitFromProto(msg.ExtCommit)
			if err != nil {
				bcR.Logger.Error("failed to convert extended commit from proto",
					"peer", e.Src,
					"err", err)
				bcR.Switch.StopPeerForError(e.Src, err, bcR.String())
				return
			}
		}

		if err := bcR.pool.AddBlock(e.Src.ID(), bi, extCommit, msg.Block.Size()); err != nil {
			bcR.Logger.Error("failed to add block", "peer", e.Src, "err", err)
		}
	case *bcproto.StatusRequest:
		// Send peer our state.
		e.Src.TrySend(p2p.Envelope{
			ChannelID: BlocksyncChannel,
			Message: &bcproto.StatusResponse{
				Height: bcR.store.Height(),
				Base:   bcR.store.Base(),
			},
		})
	case *bcproto.StatusResponse:
		// Got a peer status. Unverified.
		bcR.pool.SetPeerRange(e.Src.ID(), msg.Base, msg.Height)
	case *bcproto.NoBlockResponse:
		bcR.Logger.Debug("Peer does not have requested block", "peer", e.Src, "height", msg.Height)
		bcR.pool.RedoRequestFrom(msg.Height, e.Src.ID())
	default:
		bcR.Logger.Error(fmt.Sprintf("Unknown message type %v", reflect.TypeOf(msg)))
	}
}

// makeRandomBlockID creates a random BlockID for testing.
func makeRandomBlockID() types.BlockID {
	blockHash := make([]byte, tmhash.Size)
	partSetHash := make([]byte, tmhash.Size)
	rand.Read(blockHash)   //nolint: errcheck
	rand.Read(partSetHash) //nolint: errcheck
	return types.BlockID{Hash: blockHash, PartSetHeader: types.PartSetHeader{Total: 123, Hash: partSetHash}}
}

// makeExtCommitWithValidators creates a valid ExtendedCommit signed by the
// given validators at the specified height/round.
func makeExtCommitWithValidators(
	t *testing.T,
	blockID types.BlockID,
	height int64,
	round int32,
	valSet *types.ValidatorSet,
	privVals []types.PrivValidator,
) *types.ExtendedCommit {
	t.Helper()
	voteSet := types.NewExtendedVoteSet("test_chain_id", height, round, cmtproto.PrecommitType, valSet)
	extCommit, err := types.MakeExtCommit(blockID, height, round, voteSet, privVals, time.Now(), true)
	require.NoError(t, err)
	return extCommit
}

// TestReportedAttack_PrependByteToValidatorAddress tests the specific attack
// vector described in a security report: prepending 0xFF to a
// ValidatorAddress in an ExtendedCommit. This attack is caught during proto
// deserialization because CommitSig.ValidateBasic checks the address length.
func TestReportedAttack_PrependByteToValidatorAddress(t *testing.T) {
	height := int64(3)
	round := int32(1)

	blockID := makeRandomBlockID()
	valSet, privVals := types.RandValidatorSet(4, 1)
	extCommit := makeExtCommitWithValidators(t, blockID, height, round, valSet, privVals)
	require.NotEmpty(t, extCommit.ExtendedSignatures)

	// Apply the report's specific attack: prepend 0xFF to the first
	// validator's address. This makes the address 21 bytes instead of 20.
	poisonedProto := extCommit.ToProto()
	origAddr := poisonedProto.ExtendedSignatures[0].ValidatorAddress
	poisonedProto.ExtendedSignatures[0].ValidatorAddress = append([]byte{0xFF}, origAddr...)
	require.Len(t, poisonedProto.ExtendedSignatures[0].ValidatorAddress, crypto.AddressSize+1)

	// The attack is caught during deserialization because
	// CommitSig.ValidateBasic checks len(ValidatorAddress) == crypto.AddressSize.
	_, err := types.ExtendedCommitFromProto(poisonedProto)
	require.Error(t, err, "prepended-byte corruption must be rejected at deserialization")
}

// TestSameLengthAddressCorruption_PanicsOnRestart demonstrates the
// validation gap that the cross-validation fix addresses: a corrupted
// ExtendedCommit with a same-length (20 byte) but wrong ValidatorAddress
// passes ValidateBasic and EnsureExtensions but causes a panic during vote
// set reconstruction. This is the code path that runs in
// consensus/state.go:reconstructLastCommit on node restart.
func TestSameLengthAddressCorruption_PanicsOnRestart(t *testing.T) {
	const chainID = "test_chain_id"
	height := int64(3)
	round := int32(1)

	blockID := makeRandomBlockID()
	valSet, privVals := types.RandValidatorSet(4, 1)
	extCommit := makeExtCommitWithValidators(t, blockID, height, round, valSet, privVals)

	// Replace the first signature's ValidatorAddress with a different 20-byte
	// address. This keeps the length valid.
	poisoned := *extCommit
	sigs := make([]types.ExtendedCommitSig, len(extCommit.ExtendedSignatures))
	copy(sigs, extCommit.ExtendedSignatures)
	poisoned.ExtendedSignatures = sigs

	fakeAddr := make(crypto.Address, crypto.AddressSize)
	for i := range fakeAddr {
		fakeAddr[i] = 0xFF
	}
	poisoned.ExtendedSignatures[0].ValidatorAddress = fakeAddr

	// The poisoned ExtendedCommit passes ValidateBasic because the address
	// is the correct length.
	require.NoError(t, poisoned.ValidateBasic(),
		"same-length corrupted address should pass ValidateBasic")

	// EnsureExtensions also passes because it only checks extension presence.
	require.NoError(t, poisoned.EnsureExtensions(true),
		"same-length corrupted address should pass EnsureExtensions")

	// Proto round-trip succeeds (this is the deserialization path).
	proto := poisoned.ToProto()
	recovered, err := types.ExtendedCommitFromProto(proto)
	require.NoError(t, err,
		"same-length corrupted address should survive proto round-trip")

	// But ToExtendedVoteSet panics because AddVote checks that the address
	// matches the validator at that index. This is the same code path that
	// runs in consensus/state.go:reconstructLastCommit on node restart.
	assert.Panics(t, func() {
		recovered.ToExtendedVoteSet(chainID, valSet)
	}, "ToExtendedVoteSet must panic on address mismatch — "+
		"a poisoned ExtendedCommit in the blockstore causes a permanent node crash on restart")
}

// TestVerifyCommitDetectsAddressCorruption verifies that VerifyCommit on an
// ExtendedCommit converted via ToCommit() catches same-length ValidatorAddress
// corruption. This is the validation added in blocksync/reactor.go to
// cross-validate the ExtendedCommit before persisting.
func TestVerifyCommitDetectsAddressCorruption(t *testing.T) {
	const chainID = "test_chain_id"
	height := int64(3)
	round := int32(1)

	blockID := makeRandomBlockID()
	valSet, privVals := types.RandValidatorSet(4, 1)
	extCommit := makeExtCommitWithValidators(t, blockID, height, round, valSet, privVals)

	// Sanity: VerifyCommit passes on the uncorrupted ExtendedCommit.
	err := valSet.VerifyCommit(chainID, blockID, height, extCommit.ToCommit())
	require.NoError(t, err, "uncorrupted ExtendedCommit must pass VerifyCommit")

	// Corrupt the ExtendedCommit's first ValidatorAddress with a same-length
	// but different address.
	poisoned := *extCommit
	sigs := make([]types.ExtendedCommitSig, len(extCommit.ExtendedSignatures))
	copy(sigs, extCommit.ExtendedSignatures)
	poisoned.ExtendedSignatures = sigs

	fakeAddr := make(crypto.Address, crypto.AddressSize)
	for i := range fakeAddr {
		fakeAddr[i] = 0xFF
	}
	poisoned.ExtendedSignatures[0].ValidatorAddress = fakeAddr

	// VerifyCommit catches the corruption because it checks that each
	// signature's ValidatorAddress matches the validator at that index.
	err = valSet.VerifyCommit(chainID, blockID, height, poisoned.ToCommit())
	require.Error(t, err, "corrupted ValidatorAddress must be rejected by VerifyCommit")
}

// makeMultiValCommit builds a commit for blockID at the given height signed by
// every validator in vals.
func makeMultiValCommit(
	t *testing.T,
	height int64,
	blockID types.BlockID,
	vals *types.ValidatorSet,
	privValsByAddr map[string]types.PrivValidator,
	chainID string,
) *types.Commit {
	t.Helper()
	sigs := make([]types.CommitSig, len(vals.Validators))
	for i, val := range vals.Validators {
		pv := privValsByAddr[val.Address.String()]
		require.NotNil(t, pv, "missing privval for validator %X", val.Address)
		vote, err := types.MakeVote(pv, chainID, int32(i), height, 0, cmtproto.PrecommitType, blockID, time.Now())
		require.NoError(t, err)
		sigs[i] = vote.CommitSig()
	}
	return &types.Commit{Height: height, Round: 0, BlockID: blockID, Signatures: sigs}
}

// corruptLastSig returns a copy of commit with its last signature (past the
// +2/3 threshold) flipped to be invalid. A fresh Commit (not Clone) avoids the
// memoized hash, and the signature bytes are deep-copied so commit stays valid.
func corruptLastSig(t *testing.T, commit *types.Commit) *types.Commit {
	t.Helper()
	require.GreaterOrEqual(t, len(commit.Signatures), 4)
	sigs := append([]types.CommitSig(nil), commit.Signatures...)
	last := len(sigs) - 1
	badSig := append([]byte(nil), sigs[last].Signature...)
	badSig[0] ^= 0xFF
	sigs[last].Signature = badSig
	return &types.Commit{
		Height:     commit.Height,
		Round:      commit.Round,
		BlockID:    commit.BlockID,
		Signatures: sigs,
	}
}

// newMultiValReactor builds a block-sync reactor backed by an honest chain of
// maxBlockHeight blocks signed by all validators in genDoc. When
// invalidCommitHeight is non-zero, the reactor serves (from memory, not the
// store) a block at that height whose LastCommit has one invalid signature past
// the +2/3 threshold — used to check that block sync fully verifies the commits
// it persists.
func newMultiValReactor(
	t *testing.T,
	logger log.Logger,
	genDoc *types.GenesisDoc,
	privVals []types.PrivValidator,
	maxBlockHeight int64,
	invalidCommitHeight int64,
) ReactorPair {
	t.Helper()

	privValsByAddr := make(map[string]types.PrivValidator, len(privVals))
	for _, pv := range privVals {
		pk, err := pv.GetPubKey()
		require.NoError(t, err)
		privValsByAddr[pk.Address().String()] = pv
	}

	app := abci.NewBaseApplication()
	cc := proxy.NewLocalClientCreator(app)
	proxyApp := proxy.NewAppConns(cc, proxy.NopMetrics())
	require.NoError(t, proxyApp.Start())

	stateStore := sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{DiscardABCIResponses: false})
	blockStore := store.NewBlockStore(dbm.NewMemDB())
	state, err := stateStore.LoadFromDBOrGenesisDoc(genDoc)
	require.NoError(t, err)

	mp := &mpmocks.Mempool{}
	mp.On("Lock").Return()
	mp.On("Unlock").Return()
	mp.On("FlushAppConn", mock.Anything).Return(nil)
	mp.On("Update", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil)

	blockExec := sm.NewBlockExecutor(stateStore, log.TestingLogger(), proxyApp.Consensus(), mp, sm.EmptyEvidencePool{}, blockStore)
	require.NoError(t, stateStore.Save(state))

	chainID := state.ChainID
	lastCommit := &types.Commit{} // empty for the initial height
	var invalidBlock *types.Block
	for h := int64(1); h <= maxBlockHeight; h++ {
		block, parts, err := state.MakeBlock(h, types.MakeData([]types.Tx{}), lastCommit, nil, state.Validators.GetProposer().Address)
		require.NoError(t, err)
		blockID := types.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
		commit := makeMultiValCommit(t, h, blockID, state.Validators, privValsByAddr, chainID)

		if h == invalidCommitHeight {
			// Rebuild this block with a modified LastCommit. Rebuilding via
			// MakeBlock keeps LastCommitHash consistent, so the block passes
			// receipt validation and is rejected only by commit verification.
			invalidBlock, _, err = state.MakeBlock(h, types.MakeData([]types.Tx{}), corruptLastSig(t, lastCommit), nil, state.Validators.GetProposer().Address)
			require.NoError(t, err)
		}

		state, err = blockExec.ApplyBlock(state, blockID, block, lastCommit)
		require.NoError(t, err)
		blockStore.SaveBlock(block, parts, commit)
		lastCommit = commit
	}

	bcReactor := NewReactor(state.Copy(), blockExec, blockStore, true, NopMetrics(), 0)
	bcReactor.SetLogger(logger.With("module", "blocksync"))
	byz := NewByzantineReactor(0, bcReactor)
	byz.invalidBlock = invalidBlock
	return ReactorPair{byz, proxyApp}
}

// TestBlockSyncVerifiesTipCommitSignatures checks that block sync fully
// verifies the commit it persists for its last synced height (backport of
// cometbft/cometbft#5753). A peer serves a tip block whose LastCommit has a
// valid +2/3 majority but one invalid signature past the threshold. With the
// fix (full VerifyCommit) the receiver rejects it and drops the peer; under
// VerifyCommitLight the extra signature is never checked and it would be
// accepted.
func TestBlockSyncVerifiesTipCommitSignatures(t *testing.T) {
	config = test.ResetTestRoot("blocksync_tip_commit_sigs_test")
	defer os.RemoveAll(config.RootDir)

	// Four equal-power validators: +2/3 is reached after three signatures, so
	// the fourth (invalid) one is never inspected by VerifyCommitLight.
	genDoc, privVals := randGenesisDoc(4, false, 1)
	// Disable vote extensions so second.LastCommit is what gets persisted.
	genDoc.ConsensusParams.ABCI.VoteExtensionsEnableHeight = 0

	const maxBlockHeight = int64(10)

	// The receiver's last syncable height is maxBlockHeight-1, whose commit is
	// carried in block maxBlockHeight's LastCommit; serve that block corrupted.
	source := newMultiValReactor(t, log.TestingLogger(), genDoc, privVals, maxBlockHeight, maxBlockHeight)
	receiver := newMultiValReactor(t, log.TestingLogger(), genDoc, privVals, 0, 0)
	reactorPairs := []ReactorPair{source, receiver}

	p2p.MakeConnectedSwitches(config.P2P, 2, func(i int, s *p2p.Switch) *p2p.Switch {
		s.AddReactor("BLOCKSYNC", reactorPairs[i].reactor)
		return s
	}, p2p.Connect2Switches)

	defer func() {
		for _, r := range reactorPairs {
			_ = r.reactor.Stop()
			_ = r.app.Stop()
		}
	}()

	// Wait until the receiver either drops its only peer (fix: commit rejected)
	// or reaches maxBlockHeight-1 (accepted under partial verification).
	startTime := time.Now()
	for {
		if receiver.reactor.Switch.Peers().Size() == 0 ||
			receiver.reactor.store.Height() >= maxBlockHeight-1 {
			break
		}
		if time.Since(startTime) > 60*time.Second {
			t.Fatalf("timeout: receiver height %d, peers %d",
				receiver.reactor.store.Height(), receiver.reactor.Switch.Peers().Size())
		}
		time.Sleep(50 * time.Millisecond)
	}

	// With the fix the corrupted commit fails verification, so maxBlockHeight-1
	// is never persisted. Under VerifyCommitLight it would be, failing here.
	fmt.Println("max block height", receiver.reactor.store.Height())
	require.Less(t, receiver.reactor.store.Height(), maxBlockHeight-1,
		"block sync must not persist a height whose commit has an invalid signature past the +2/3 threshold")
}
