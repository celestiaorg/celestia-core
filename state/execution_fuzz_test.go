package state_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/celestiaorg/go-square/v4/share"
	squaretx "github.com/celestiaorg/go-square/v4/tx"
	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	abci "github.com/cometbft/cometbft/abci/types"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/evidence"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/mempool"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/proxy"
	sm "github.com/cometbft/cometbft/state"
	"github.com/cometbft/cometbft/store"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	fuzzExecChainID = "state-exec-fuzz"
	fuzzExecVals    = 4
	fuzzExecPower   = 1000
	fuzzExecMaxTxs  = 16
	fuzzExecMaxOps  = 8
	fuzzExecMaxIn   = 4096
)

// fuzzExecGenesisTime is fixed so that every execution builds the same chain.
var fuzzExecGenesisTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// fuzzExecApp is a deterministic application. ProcessProposal binds the data
// root to the txs the way celestia-app binds it to the data square, and
// FinalizeBlock derives results and the app hash from the txs it receives.
type fuzzExecApp struct {
	abci.BaseApplication

	ppReq   *abci.RequestProcessProposal
	finTxs  [][]byte
	appHash []byte
	vveReq  *abci.RequestVerifyVoteExtension
}

func (app *fuzzExecApp) ProcessProposal(_ context.Context, req *abci.RequestProcessProposal) (*abci.ResponseProcessProposal, error) {
	app.ppReq = req
	if bytes.Equal(req.DataRootHash, cmttypes.ToTxs(req.Txs).Hash()) && req.SquareSize == uint64(len(req.Txs)) {
		return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_ACCEPT}, nil
	}
	return &abci.ResponseProcessProposal{Status: abci.ResponseProcessProposal_REJECT}, nil
}

func (app *fuzzExecApp) FinalizeBlock(_ context.Context, req *abci.RequestFinalizeBlock) (*abci.ResponseFinalizeBlock, error) {
	app.finTxs = req.Txs
	h := sha256.New()
	h.Write(app.appHash)
	h.Write(binary.BigEndian.AppendUint64(nil, uint64(req.Height)))
	results := make([]*abci.ExecTxResult, len(req.Txs))
	for i, tx := range req.Txs {
		h.Write(binary.BigEndian.AppendUint32(nil, uint32(len(tx))))
		h.Write(tx)
		results[i] = &abci.ExecTxResult{Code: uint32(len(tx) % 3), Data: tx[:min(len(tx), 4)]}
	}
	app.appHash = h.Sum(nil)
	return &abci.ResponseFinalizeBlock{TxResults: results, AppHash: app.appHash}, nil
}

func (app *fuzzExecApp) VerifyVoteExtension(_ context.Context, req *abci.RequestVerifyVoteExtension) (*abci.ResponseVerifyVoteExtension, error) {
	app.vveReq = req
	if len(req.VoteExtension) > 0 && req.VoteExtension[0] == byte(req.Height) {
		return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_ACCEPT}, nil
	}
	return &abci.ResponseVerifyVoteExtension{Status: abci.ResponseVerifyVoteExtension_REJECT}, nil
}

// fuzzExecKeys holds deterministic validator keys, indexed by validator set
// position.
type fuzzExecKeys struct {
	privVals []cmttypes.PrivValidator
	genVals  []cmttypes.GenesisValidator
	valSet   *cmttypes.ValidatorSet
}

func newFuzzExecKeys() fuzzExecKeys {
	var k fuzzExecKeys
	vals := make([]*cmttypes.Validator, fuzzExecVals)
	pvs := make(map[string]cmttypes.PrivValidator, fuzzExecVals)
	for i := range vals {
		pk := ed25519.GenPrivKeyFromSecret([]byte(fmt.Sprintf("state-exec-fuzz-%d", i)))
		vals[i] = cmttypes.NewValidator(pk.PubKey(), fuzzExecPower)
		pvs[pk.PubKey().Address().String()] = cmttypes.NewMockPVWithParams(pk, false, false)
		k.genVals = append(k.genVals, cmttypes.GenesisValidator{
			Address: pk.PubKey().Address(), PubKey: pk.PubKey(), Power: fuzzExecPower,
		})
	}
	k.valSet = cmttypes.NewValidatorSet(vals)
	for _, v := range k.valSet.Validators {
		k.privVals = append(k.privVals, pvs[v.Address.String()])
	}
	return k
}

func (k fuzzExecKeys) vote(t *testing.T, idx int, height int64, typ cmtproto.SignedMsgType,
	blockID cmttypes.BlockID, ts time.Time,
) *cmttypes.Vote {
	v, err := cmttypes.MakeVote(k.privVals[idx], fuzzExecChainID, int32(idx), height, 0, typ, blockID, ts)
	require.NoError(t, err)
	return v
}

// fuzzExecChain is a fresh chain that has committed block 1.
type fuzzExecChain struct {
	app        *fuzzExecApp
	state      sm.State
	stateStore sm.Store
	blockStore *store.BlockStore
	blockExec  *sm.BlockExecutor
	block1ID   cmttypes.BlockID
	block1Time time.Time
}

func newFuzzExecChain(t *testing.T, k fuzzExecKeys, appVersion uint64) *fuzzExecChain {
	params := cmttypes.DefaultConsensusParams()
	params.Version.App = appVersion
	genDoc := &cmttypes.GenesisDoc{
		ChainID:         fuzzExecChainID,
		GenesisTime:     fuzzExecGenesisTime,
		InitialHeight:   1,
		ConsensusParams: params,
		Validators:      k.genVals,
	}
	require.NoError(t, genDoc.ValidateAndComplete())
	state, err := sm.MakeGenesisState(genDoc)
	require.NoError(t, err)
	stateStore := sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{})
	require.NoError(t, stateStore.Save(state))
	blockStore := store.NewBlockStore(dbm.NewMemDB())
	evpool, err := evidence.NewPool(dbm.NewMemDB(), stateStore, blockStore)
	require.NoError(t, err)

	app := &fuzzExecApp{}
	proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(app), proxy.NopMetrics())
	require.NoError(t, proxyApp.Start())
	t.Cleanup(func() { _ = proxyApp.Stop() })
	blockExec := sm.NewBlockExecutor(stateStore, log.NewNopLogger(), proxyApp.Consensus(),
		&mempool.NopMempool{}, evpool, blockStore)

	block1, parts1, err := state.MakeBlock(1, cmttypes.Data{}, &cmttypes.Commit{}, nil, state.Validators.GetProposer().Address)
	require.NoError(t, err)
	block1ID := cmttypes.BlockID{Hash: block1.Hash(), PartSetHeader: parts1.Header()}
	blockStore.SaveBlock(block1, parts1, fullCommit(t, k, 1, block1ID, block1.Time.Add(time.Second)))
	state, err = blockExec.ApplyBlock(state, block1ID, block1, nil)
	require.NoError(t, err)
	return &fuzzExecChain{app, state, stateStore, blockStore, blockExec, block1ID, block1.Time}
}

// fullCommit is a commit signed by every validator at the same time.
func fullCommit(t *testing.T, k fuzzExecKeys, height int64, blockID cmttypes.BlockID, ts time.Time) *cmttypes.Commit {
	sigs := make([]cmttypes.CommitSig, fuzzExecVals)
	for i := range sigs {
		sigs[i] = k.vote(t, i, height, cmtproto.PrecommitType, blockID, ts).CommitSig()
	}
	return &cmttypes.Commit{Height: height, BlockID: blockID, Signatures: sigs}
}

// fuzzExecReader hands out fuzz bytes, returning zero once they run out.
type fuzzExecReader []byte

func (r *fuzzExecReader) byte() byte {
	if len(*r) == 0 {
		return 0
	}
	b := (*r)[0]
	*r = (*r)[1:]
	return b
}

func (r *fuzzExecReader) bytes(n int) []byte {
	n = min(n, len(*r))
	b := (*r)[:n]
	*r = (*r)[n:]
	return b
}

// fuzzExecTxs builds up to fuzzExecMaxTxs txs of the shapes a block carries:
// plain bytes, empty txs, go-square BlobTxs, legacy-encoded BlobTxs with a
// fuzzed namespace ID length, and IndexWrappers.
func fuzzExecTxs(spec []byte) cmttypes.Txs {
	r := fuzzExecReader(spec)
	var txs cmttypes.Txs
	for len(r) > 0 && len(txs) < fuzzExecMaxTxs {
		kind, payload := r.byte(), r.bytes(int(r.byte()))
		inner := append([]byte("tx"), payload...)
		var tx []byte
		switch kind % 5 {
		case 0:
			tx = bytes.Clone(payload)
		case 1:
			tx = []byte{}
		case 2:
			ns, err := share.NewV0Namespace(payload[:min(len(payload), 10)])
			if err != nil {
				continue
			}
			blob, err := share.NewV0Blob(ns, append([]byte{kind}, payload...))
			if err != nil {
				continue
			}
			if tx, err = squaretx.MarshalBlobTx(inner, blob); err != nil {
				continue
			}
		case 3:
			nsLen := int(kind>>3) % (share.NamespaceIDSize + 4)
			//nolint:staticcheck // the legacy message is what pre-v11 blocks carry
			btx := &cmtproto.BlobTx{
				Tx:     inner,
				Blobs:  []*cmtproto.Blob{{NamespaceId: make([]byte, nsLen), Data: payload}},
				TypeId: squaretx.ProtoBlobTxTypeID,
			}
			tx, _ = btx.Marshal()
		case 4:
			iw := &cmtproto.IndexWrapper{Tx: inner, ShareIndexes: []uint32{uint32(kind)}, TypeId: squaretx.ProtoIndexWrapperTypeID}
			tx, _ = iw.Marshal()
		}
		txs = append(txs, tx)
	}
	return txs
}

// wantUnwrapped is the tx FinalizeBlock must receive: the go-square decoder's
// inner tx from app version 11, and the generated legacy decoder's before it.
func wantUnwrapped(tx []byte, appVersion uint64) []byte {
	if appVersion >= cmttypes.BlobTxValidationAppVersion {
		if btx, isBlob, err := squaretx.UnmarshalBlobTx(tx); isBlob && err == nil {
			return btx.Tx
		}
		return tx
	}
	var btx cmtproto.BlobTx //nolint:staticcheck // legacy rules
	if btx.Unmarshal(tx) != nil || btx.TypeId != squaretx.ProtoBlobTxTypeID || len(btx.Blobs) == 0 {
		return tx
	}
	for _, b := range btx.Blobs {
		if len(b.NamespaceId) != share.NamespaceIDSize {
			return tx
		}
	}
	return btx.Tx
}

// FuzzBlockExecValidateApply builds the next block of a four-validator chain
// from fuzzed txs, LastCommit votes and evidence, mutates header and data
// fields, decodes the block the way it arrives from a peer, and runs it
// through ValidateBlock (and the blocksync variant), ProcessProposal and
// ApplyVerifiedBlock, then checks that the chain can commit one more block.
func FuzzBlockExecValidateApply(f *testing.F) {
	f.Add(uint8(0), []byte{}, []byte{0, 3, 'a', 'b', 'c', 2, 4, 1, 2, 3, 4}, []byte{}, []byte{})
	f.Add(uint8(1), []byte{0, 5, 1, 9, 2, 0, 3, 0}, []byte{3, 8, 1, 2, 3, 4, 5, 6, 7, 8, 4, 1, 9, 1, 0}, []byte{15, 0, 1}, []byte{})
	f.Add(uint8(4), []byte{}, []byte{}, []byte{}, []byte{1, 0})
	f.Add(uint8(2), []byte{0, 0, 0, 0, 3, 0}, []byte{0, 1, 7}, []byte{17, 0, 0, 21, 0, 0}, []byte{2, 3, 5})
	f.Add(uint8(0), []byte{}, []byte{0, 1, 7}, append(append([]byte{8, 32}, bytes.Repeat([]byte{7}, 32)...), 0), []byte{})

	keys := newFuzzExecKeys()
	f.Fuzz(func(t *testing.T, flags uint8, commitSpec, txSpec, headerOps, evSpec []byte) {
		if len(commitSpec)+len(txSpec)+len(headerOps)+len(evSpec) > fuzzExecMaxIn {
			return
		}
		appVersion := uint64(10)
		if flags&1 != 0 {
			appVersion = cmttypes.BlobTxValidationAppVersion
		}
		c := newFuzzExecChain(t, keys, appVersion)
		state := c.state

		// LastCommit: per validator a flag and a timestamp offset, then
		// corruptions that are not re-signed.
		cr := fuzzExecReader(commitSpec)
		sigs := make([]cmttypes.CommitSig, 0, fuzzExecVals+1)
		genuine := 0
		for i := 0; i < fuzzExecVals; i++ {
			flag, off := cr.byte(), time.Duration(int8(cr.byte()))*time.Second
			ts := c.block1Time.Add(time.Second + off)
			switch flag % 4 {
			case 0, 1:
				sigs = append(sigs, keys.vote(t, i, 1, cmtproto.PrecommitType, c.block1ID, ts).CommitSig())
				genuine++
			case 2:
				sigs = append(sigs, keys.vote(t, i, 1, cmtproto.PrecommitType, cmttypes.BlockID{}, ts).CommitSig())
			case 3:
				sigs = append(sigs, cmttypes.NewCommitSigAbsent())
			}
		}
		commit := &cmttypes.Commit{Height: 1, BlockID: c.block1ID, Signatures: sigs}
		corrupt := false
		for n := 0; n < fuzzExecMaxOps/2 && len(cr) > 0; n++ {
			op, arg := cr.byte(), int(cr.byte())
			s := &commit.Signatures[arg%len(commit.Signatures)]
			switch op % 8 {
			case 0:
				if len(s.Signature) > 0 {
					s.Signature = bytes.Clone(s.Signature)
					s.Signature[arg%len(s.Signature)] ^= 1 << (op >> 5)
					corrupt = true
				}
			case 1:
				if s.BlockIDFlag != cmttypes.BlockIDFlagAbsent {
					s.ValidatorAddress = keys.valSet.Validators[(arg/7)%fuzzExecVals].Address
				}
			case 2:
				switch s.BlockIDFlag {
				case cmttypes.BlockIDFlagCommit:
					s.BlockIDFlag = cmttypes.BlockIDFlagNil
				case cmttypes.BlockIDFlagNil:
					s.BlockIDFlag = cmttypes.BlockIDFlagCommit
				}
			case 3:
				commit.Signatures = commit.Signatures[:len(commit.Signatures)-1]
			case 4:
				commit.Signatures = append(commit.Signatures, cmttypes.NewCommitSigAbsent())
			case 5:
				commit.Round += int32(arg%3) + 1
			case 6:
				commit.Height += int64(arg%3) + 1
			case 7:
				commit.BlockID.PartSetHeader.Total += uint32(arg%3) + 1
			}
			corrupt = corrupt || op%8 != 0
			if len(commit.Signatures) == 0 {
				return
			}
		}

		// Evidence: duplicate votes from one validator at height 1.
		var evList []cmttypes.Evidence
		evMutated := false
		if flags&4 != 0 {
			er := fuzzExecReader(evSpec)
			idx, op, arg := int(er.byte())%fuzzExecVals, er.byte(), int64(int8(er.byte()))
			idA := cmttypes.BlockID{Hash: bytes.Repeat([]byte{1}, 32), PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: bytes.Repeat([]byte{2}, 32)}}
			idB := cmttypes.BlockID{Hash: bytes.Repeat([]byte{3}, 32), PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: bytes.Repeat([]byte{4}, 32)}}
			heightB := int64(1)
			switch op % 8 {
			case 4:
				idB = idA
			case 5:
				heightB = 2
			}
			vA := keys.vote(t, idx, 1, cmtproto.PrevoteType, idA, c.block1Time)
			vB := keys.vote(t, idx, heightB, cmtproto.PrevoteType, idB, c.block1Time)
			dve, err := cmttypes.NewDuplicateVoteEvidence(vA, vB, c.block1Time, keys.valSet)
			if err != nil {
				return
			}
			switch op % 8 {
			case 1:
				dve.Timestamp = dve.Timestamp.Add(time.Duration(arg) * time.Second)
			case 2:
				dve.ValidatorPower += arg
			case 3:
				dve.TotalVotingPower += arg
			case 6:
				dve.VoteA.Signature = bytes.Clone(dve.VoteA.Signature)
				dve.VoteA.Signature[0] ^= 1
			}
			evMutated = op%8 != 0 && op%8 != 7
			evList = append(evList, dve)
			if op%8 == 7 {
				evList = append(evList, dve)
				evMutated = true
			}
		}

		txs := fuzzExecTxs(txSpec)
		honest, _, err := state.MakeBlock(2, cmttypes.NewData(txs, uint64(len(txs)), nil), commit, evList, keys.valSet.Validators[int(flags>>4)%fuzzExecVals].Address)
		if err != nil {
			return
		}
		honestHash := honest.Hash()

		// Header and data mutations a byzantine proposer can make.
		block := honest
		hr := fuzzExecReader(headerOps)
		for n := 0; n < fuzzExecMaxOps && len(hr) > 0; n++ {
			op, val := hr.byte(), hr.bytes(int(hr.byte())%40)
			d := int64(int8(hr.byte())) | 1
			switch op % 22 {
			case 0:
				block.Version.Block += uint64(d)
			case 1:
				block.Version.App = uint64(d)
			case 2:
				block.ChainID = string(val)
			case 3:
				block.Height += d
			case 4:
				block.Time = block.Time.Add(time.Duration(d))
			case 5:
				block.LastBlockID.PartSetHeader.Total += uint32(d)
			case 6:
				block.LastCommitHash = val
			case 7:
				block.DataHash = val
			case 8:
				block.DataHash = val
				block.Data = cmttypes.NewData(block.Txs, block.SquareSize, val)
			case 9:
				block.ValidatorsHash = val
			case 10:
				block.NextValidatorsHash = val
			case 11:
				block.ConsensusHash = val
			case 12:
				block.AppHash = val
			case 13:
				block.LastResultsHash = val
			case 14:
				block.EvidenceHash = val
			case 15:
				block.ProposerAddress = keys.valSet.Validators[int(d&0x7f)%fuzzExecVals].Address
			case 16:
				block.ProposerAddress = val
			case 17:
				if len(block.Txs) > 0 && len(block.Txs[int(d&0x7f)%len(block.Txs)]) > 0 {
					tx := bytes.Clone(block.Txs[int(d&0x7f)%len(block.Txs)])
					tx[len(val)%len(tx)] ^= byte(d)
					block.Txs[int(d&0x7f)%len(block.Txs)] = tx
				}
			case 18:
				if len(block.Txs) > 0 {
					block.Txs = block.Txs[:len(block.Txs)-1]
				}
			case 19:
				if len(block.Txs) < fuzzExecMaxTxs {
					block.Txs = append(block.Txs, bytes.Clone(val))
				}
			case 20:
				block.SquareSize = uint64(d)
			case 21:
				// Honest proposer of a different tx set: rebind both hashes.
				block.Data = cmttypes.NewData(block.Txs, uint64(len(block.Txs)), nil)
				block.DataHash = block.Data.Hash()
			}
		}

		// Deliver the block the way peers do: as protobuf.
		pb, err := block.ToProto()
		require.NoError(t, err)
		block, err = cmttypes.BlockFromProto(pb)
		if err != nil {
			return
		}

		err = c.blockExec.ValidateBlock(state, block)
		// Blocksync verifies LastCommit separately, then skips it.
		bsErr := state.LastValidators.VerifyCommit(fuzzExecChainID, state.LastBlockID, block.Height-1, block.LastCommit)
		if bsErr == nil {
			bsErr = c.blockExec.ValidateBlockSkipLastCommit(state, block)
		}
		require.Equal(t, err == nil, bsErr == nil, "ValidateBlock=%v, blocksync path=%v", err, bsErr)

		unchanged := bytes.Equal(block.Hash(), honestHash) && bytes.Equal(block.Data.Hash(), honest.Txs.Hash()) &&
			bytes.Equal(block.Txs.Hash(), honest.Txs.Hash()) && block.SquareSize == uint64(len(block.Txs))
		wantValid := unchanged && !corrupt && !evMutated && genuine*3 > fuzzExecVals*2 && block.Time.After(c.block1Time)
		if wantValid {
			require.NoError(t, err, "honest block rejected")
		}
		if err != nil {
			return
		}

		require.GreaterOrEqual(t, genuine*3, fuzzExecVals*2+1, "LastCommit without quorum accepted")
		require.True(t, lastCommitAllValid(t, keys, block.LastCommit), "LastCommit with an invalid signature accepted")
		for _, ev := range block.Evidence.Evidence {
			dve, ok := ev.(*cmttypes.DuplicateVoteEvidence)
			require.True(t, ok)
			require.Equal(t, int64(fuzzExecPower), dve.ValidatorPower)
			require.Equal(t, int64(fuzzExecPower*fuzzExecVals), dve.TotalVotingPower)
			require.Equal(t, c.block1Time, dve.Timestamp)
			require.Equal(t, dve.VoteA.Height, dve.VoteB.Height)
			require.False(t, dve.VoteA.BlockID.Equals(dve.VoteB.BlockID))
			// Both votes must carry the implicated validator's signature over
			// their canonical sign bytes; metadata alone does not prove it.
			_, val := state.LastValidators.GetByAddress(dve.VoteA.ValidatorAddress)
			require.NotNil(t, val, "evidence against a non-validator accepted")
			require.NoError(t, dve.VoteA.Verify(fuzzExecChainID, val.PubKey), "evidence VoteA signature invalid")
			require.NoError(t, dve.VoteB.Verify(fuzzExecChainID, val.PubKey), "evidence VoteB signature invalid")
		}
		// Every state-derived header field is what the state dictates: only
		// txs, data root, evidence, proposer and LastCommit are the proposer's.
		rebuilt, err := state.MakeBlockWithoutPartset(2, cmttypes.NewData(block.Txs, block.SquareSize, nil),
			block.LastCommit, block.Evidence.Evidence, block.ProposerAddress)
		require.NoError(t, err)
		rebuilt.DataHash = block.DataHash
		require.Equal(t, rebuilt.Header.Hash(), block.Header.Hash(), "header field not bound to state")

		accepted, err := c.blockExec.ProcessProposal(block, state.InitialHeight)
		require.NoError(t, err)
		req := c.app.ppReq
		require.Equal(t, []byte(block.Header.Hash()), req.Hash)
		require.Equal(t, []byte(block.DataHash), req.DataRootHash)
		require.Equal(t, block.Txs.ToSliceOfBytes(), req.Txs)
		require.Len(t, req.ProposedLastCommit.Votes, fuzzExecVals)
		require.Len(t, req.Misbehavior, len(block.Evidence.Evidence))
		if wantValid {
			require.True(t, accepted, "honest block rejected by ProcessProposal")
		}
		if !accepted {
			return
		}
		rebuilt.DataHash = nil
		require.Equal(t, rebuilt.Hash(), block.Hash(), "accepted block whose data hash does not bind its txs")

		// Commit and execute the block, then check the chain can continue.
		parts, err := block.MakePartSet(cmttypes.BlockPartSizeBytes)
		require.NoError(t, err)
		blockID := cmttypes.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}
		commit2 := fullCommit(t, keys, 2, blockID, block.Time.Add(time.Second))
		c.blockStore.SaveBlock(block, parts, commit2)
		prevAppHash := state.AppHash
		state, err = c.blockExec.ApplyVerifiedBlock(state, blockID, block, commit2)
		require.NoError(t, err)
		require.EqualValues(t, 2, state.LastBlockHeight)
		require.Equal(t, blockID, state.LastBlockID)
		require.Equal(t, block.Time, state.LastBlockTime)
		require.Equal(t, c.app.appHash, state.AppHash)
		require.NotEqual(t, prevAppHash, state.AppHash)
		loaded, err := c.stateStore.Load()
		require.NoError(t, err)
		require.Equal(t, state.LastBlockHeight, loaded.LastBlockHeight)
		require.Equal(t, state.AppHash, loaded.AppHash)

		require.Len(t, c.app.finTxs, len(block.Txs))
		seen := make(map[string]int, len(block.Txs))
		for i, tx := range block.Txs {
			require.Equal(t, wantUnwrapped(tx, appVersion), c.app.finTxs[i], "tx %d unwrapped wrongly", i)
			seen[string(tx.Hash())]++
		}
		for i, tx := range block.Txs {
			if seen[string(tx.Hash())] != 1 {
				continue
			}
			info := c.blockStore.LoadTxInfo(tx.Hash())
			require.NotNil(t, info, "tx %d not indexed", i)
			require.EqualValues(t, 2, info.Height)
			require.EqualValues(t, i, info.Index)
			require.EqualValues(t, len(c.app.finTxs[i])%3, info.Code)
		}

		block3, parts3, err := state.MakeBlock(3, cmttypes.NewData(nil, 0, nil), commit2, nil, state.Validators.GetProposer().Address)
		require.NoError(t, err)
		require.NoError(t, c.blockExec.ValidateBlock(state, block3), "chain cannot continue after block 2")
		accepted, err = c.blockExec.ProcessProposal(block3, state.InitialHeight)
		require.NoError(t, err)
		require.True(t, accepted)
		block3ID := cmttypes.BlockID{Hash: block3.Hash(), PartSetHeader: parts3.Header()}
		c.blockStore.SaveBlock(block3, parts3, fullCommit(t, keys, 3, block3ID, block3.Time.Add(time.Second)))
		state, err = c.blockExec.ApplyVerifiedBlock(state, block3ID, block3, nil)
		require.NoError(t, err)
		require.EqualValues(t, 3, state.LastBlockHeight)
	})
}

// lastCommitAllValid independently checks every non-absent signature of a
// LastCommit for block 1.
func lastCommitAllValid(t *testing.T, k fuzzExecKeys, commit *cmttypes.Commit) bool {
	t.Helper()
	for i, s := range commit.Signatures {
		if s.BlockIDFlag == cmttypes.BlockIDFlagAbsent {
			continue
		}
		if i >= fuzzExecVals || !bytes.Equal(s.ValidatorAddress, k.valSet.Validators[i].Address) {
			return false
		}
		v := commit.GetVote(int32(i))
		if !k.valSet.Validators[i].PubKey.VerifySignature(cmttypes.VoteSignBytes(fuzzExecChainID, v.ToProto()), s.Signature) {
			return false
		}
	}
	return true
}

// FuzzVerifyVoteExtension sends a fuzzed precommit through the checks the
// consensus state runs before BlockExecutor.VerifyVoteExtension, then through
// VerifyVoteExtension itself.
func FuzzVerifyVoteExtension(f *testing.F) {
	f.Add(uint8(0), int64(5), int32(0), []byte{5, 1, 2}, uint8(0), []byte{})
	f.Add(uint8(1), int64(7), int32(1), []byte{6}, uint8(1), []byte{7})
	f.Add(uint8(2), int64(9), int32(0), []byte{}, uint8(4), []byte{3})
	f.Add(uint8(3), int64(1), int32(2), []byte{1}, uint8(6), []byte{0})

	keys := newFuzzExecKeys()
	f.Fuzz(func(t *testing.T, signer uint8, height int64, round int32, ext []byte, mutate uint8, arg []byte) {
		if len(ext) > 1024 || len(arg) > 1024 || height <= 0 || round < 0 {
			return
		}
		app := &fuzzExecApp{}
		proxyApp := proxy.NewAppConns(proxy.NewLocalClientCreator(app), proxy.NopMetrics())
		require.NoError(t, proxyApp.Start())
		t.Cleanup(func() { _ = proxyApp.Stop() })
		blockExec := sm.NewBlockExecutor(sm.NewStore(dbm.NewMemDB(), sm.StoreOptions{}), log.NewNopLogger(),
			proxyApp.Consensus(), &mempool.NopMempool{}, sm.EmptyEvidencePool{}, store.NewBlockStore(dbm.NewMemDB()))

		idx := int(signer) % fuzzExecVals
		blockID := cmttypes.BlockID{Hash: bytes.Repeat([]byte{9}, 32), PartSetHeader: cmttypes.PartSetHeader{Total: 1, Hash: bytes.Repeat([]byte{8}, 32)}}
		vote := &cmttypes.Vote{
			Type: cmtproto.PrecommitType, Height: height, Round: round, BlockID: blockID,
			Timestamp: fuzzExecGenesisTime, ValidatorAddress: keys.valSet.Validators[idx].Address,
			ValidatorIndex: int32(idx), Extension: ext,
		}
		pv := vote.ToProto()
		require.NoError(t, keys.privVals[idx].SignVote(fuzzExecChainID, pv))
		vote.Signature, vote.ExtensionSignature = pv.Signature, pv.ExtensionSignature

		a := fuzzExecReader(arg)
		extBound := true // whether the extension signature still covers the vote
		switch mutate % 10 {
		case 1:
			vote.Extension = arg
			extBound = bytes.Equal(arg, ext)
		case 2:
			vote.Height++
			extBound = false
		case 3:
			vote.Round++
			extBound = false
		case 4:
			vote.ValidatorIndex = int32(a.byte())
			extBound = int(vote.ValidatorIndex) == idx
		case 5:
			vote.ValidatorAddress = keys.valSet.Validators[int(a.byte())%fuzzExecVals].Address
		case 6:
			vote.ExtensionSignature = bytes.Clone(vote.ExtensionSignature)
			vote.ExtensionSignature[int(a.byte())%len(vote.ExtensionSignature)] ^= 1
			extBound = false
		case 7:
			vote.BlockID.Hash = bytes.Repeat([]byte{a.byte()}, 32)
		case 8:
			vote.Type = cmtproto.PrevoteType
		case 9:
			vote.ExtensionSignature = arg
			extBound = bytes.Equal(arg, pv.ExtensionSignature)
		}

		// Reactor and consensus-state checks that precede VerifyVoteExtension.
		if vote.ValidateBasic() != nil {
			return
		}
		if vote.Type != cmtproto.PrecommitType || vote.BlockID.IsZero() {
			return
		}
		_, val := keys.valSet.GetByIndex(vote.ValidatorIndex)
		if val == nil {
			return
		}
		if err := vote.VerifyExtension(fuzzExecChainID, val.PubKey); err != nil {
			// Only a mutation of a signed field (extension, height, round,
			// index or the signature itself) may make verification fail.
			require.False(t, extBound, "valid extension signature rejected: %v", err)
			return
		}
		require.True(t, extBound, "extension signature accepted for altered vote")

		err := blockExec.VerifyVoteExtension(context.Background(), vote)
		req := app.vveReq
		require.NotNil(t, req)
		require.Equal(t, []byte(vote.BlockID.Hash), req.Hash)
		require.Equal(t, []byte(vote.ValidatorAddress), req.ValidatorAddress)
		require.Equal(t, vote.Height, req.Height)
		require.Equal(t, vote.Extension, req.VoteExtension)
		wantOK := len(vote.Extension) > 0 && vote.Extension[0] == byte(vote.Height)
		if wantOK {
			require.NoError(t, err)
		} else {
			require.ErrorIs(t, err, cmttypes.ErrInvalidVoteExtension)
		}
	})
}
