package light_test

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	dbm "github.com/cometbft/cometbft-db"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/light"
	"github.com/cometbft/cometbft/light/provider"
	dbs "github.com/cometbft/cometbft/light/store/db"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmtversion "github.com/cometbft/cometbft/proto/tendermint/version"
	"github.com/cometbft/cometbft/types"
	"github.com/cometbft/cometbft/version"
)

const (
	fzChainID = "fuzz-light"
	fzHeights = 6
	// fzMaxCalls bounds the provider requests one input may cause.
	fzMaxCalls = 400
)

var (
	fzTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	fzNow  = fzTime.Add((fzHeights + 1) * time.Minute)
	// fzKeys[0] is the only byzantine validator; it is in every validator set
	// with a quarter of the power, below both the 1/3 trust level and 2/3.
	// fzKeys[1:9] are honest; fzKeys[9:] are attacker keys outside every set.
	fzKeys = func() []crypto.PrivKey {
		keys := make([]crypto.PrivKey, 12)
		for i := range keys {
			keys[i] = ed25519.GenPrivKeyFromSecret([]byte{'l', byte(i)})
		}
		return keys
	}()
	fzByzantine = []int{0, 9, 10, 11}
	fzPartsHash = hash("parts")
	fzHonest    = fzGenChain()
)

// fzValSet returns the honest validator set at height h. Sets rotate so that
// heights two apart share half of their power and heights three apart share
// only the byzantine key, which forces bisection in skipping mode.
func fzValSet(h int64) *types.ValidatorSet {
	idx := []int{0, 1 + int(h%8), 1 + int((h+1)%8), 1 + int((h+2)%8)}
	return fzValSetOf(idx)
}

func fzValSetOf(idx []int) *types.ValidatorSet {
	vals := make([]*types.Validator, len(idx))
	for i, k := range idx {
		vals[i] = types.NewValidator(fzKeys[k].PubKey(), 10)
	}
	return types.NewValidatorSet(vals)
}

// fzSign builds a commit for header signed by every member of vals whose key
// is in signers; the others are absent.
func fzSign(header *types.Header, vals *types.ValidatorSet, signers []int) *types.Commit {
	blockID := types.BlockID{Hash: header.Hash(), PartSetHeader: types.PartSetHeader{Total: 1, Hash: fzPartsHash}}
	sigs := make([]types.CommitSig, vals.Size())
	for i, val := range vals.Validators {
		sigs[i] = types.NewCommitSigAbsent()
		for _, k := range signers {
			key := fzKeys[k]
			if !bytes.Equal(key.PubKey().Address(), val.Address) {
				continue
			}
			vote := &types.Vote{
				Type: cmtproto.PrecommitType, Height: header.Height, Round: 1, BlockID: blockID,
				Timestamp: header.Time, ValidatorAddress: val.Address, ValidatorIndex: int32(i),
			}
			sig, err := key.Sign(types.VoteSignBytes(header.ChainID, vote.ToProto()))
			if err != nil {
				panic(err)
			}
			vote.Signature = sig
			sigs[i] = vote.CommitSig()
		}
	}
	return &types.Commit{Height: header.Height, Round: 1, BlockID: blockID, Signatures: sigs}
}

// fzGenChain deterministically builds the honest chain at heights 1..fzHeights.
func fzGenChain() map[int64]*types.LightBlock {
	chain := make(map[int64]*types.LightBlock, fzHeights)
	all := []int{0, 1, 2, 3, 4, 5, 6, 7, 8}
	var last types.BlockID
	for h := int64(1); h <= fzHeights; h++ {
		vals := fzValSet(h)
		header := &types.Header{
			Version: cmtversion.Consensus{Block: version.BlockProtocol}, ChainID: fzChainID, Height: h,
			Time: fzTime.Add(time.Duration(h) * time.Minute), LastBlockID: last,
			ValidatorsHash: vals.Hash(), NextValidatorsHash: fzValSet(h + 1).Hash(),
			AppHash: hash("app"), ConsensusHash: hash("cons"), LastResultsHash: hash("res"),
			ProposerAddress: vals.GetProposer().Address,
		}
		sh := &types.SignedHeader{Header: header, Commit: fzSign(header, vals, all)}
		chain[h] = &types.LightBlock{SignedHeader: sh, ValidatorSet: vals}
		last = sh.Commit.BlockID
	}
	return chain
}

// fzCopy deep-copies a light block through its wire encoding, so that
// tampering never aliases the honest chain's byte slices.
func fzCopy(lb *types.LightBlock) (*types.LightBlock, error) {
	pb, err := lb.ToProto()
	if err != nil {
		return nil, err
	}
	raw, err := pb.Marshal()
	if err != nil {
		return nil, err
	}
	var out cmtproto.LightBlock
	if err := out.Unmarshal(raw); err != nil {
		return nil, err
	}
	return types.LightBlockFromProto(&out)
}

// fzProvider serves a fixed, possibly tampered, set of light blocks and
// validates them the way the HTTP provider does. Its call count is shared by
// all providers of one input.
type fzProvider struct {
	blocks map[int64]*types.LightBlock
	errs   map[int64]error
	calls  *atomic.Int64
}

func (p *fzProvider) ChainID() string { return fzChainID }

func (p *fzProvider) LightBlock(_ context.Context, height int64) (*types.LightBlock, error) {
	if p.calls.Add(1) > fzMaxCalls {
		// Stop a looping client; the call count assertion reports it.
		return nil, provider.ErrNoResponse
	}
	requested := height
	if height == 0 {
		height = fzHeights
	}
	if err, ok := p.errs[height]; ok {
		return nil, err
	}
	if height > fzHeights {
		return nil, provider.ErrHeightTooHigh
	}
	lb, ok := p.blocks[height]
	if !ok {
		return nil, provider.ErrLightBlockNotFound
	}
	lb, err := fzCopy(lb)
	if err != nil {
		return nil, provider.ErrBadLightBlock{Reason: err}
	}
	if err := lb.ValidateBasic(fzChainID); err != nil {
		return nil, provider.ErrBadLightBlock{Reason: err}
	}
	// The HTTP provider, which state sync also uses, rejects a block for
	// another height; the client relies on that.
	if requested != 0 && lb.Height != requested {
		return nil, provider.ErrBadLightBlock{Reason: errors.New("height mismatch")}
	}
	return lb, nil
}

func (p *fzProvider) ReportEvidence(context.Context, types.Evidence) error { return nil }

// fzTamper applies one mutation to the provider's block at a height. Forged
// headers can only be re-signed with the byzantine keys.
func fzTamper(p *fzProvider, height int64, kind, param byte) {
	lb := p.blocks[height]
	h := lb.Header
	resign := func(vals *types.ValidatorSet) {
		lb.ValidatorSet = vals
		h.ValidatorsHash = vals.Hash()
		lb.Commit = fzSign(h, vals, fzByzantine)
	}
	editHeader := func() {
		switch param % 8 {
		case 0:
			h.AppHash = hash(string([]byte{param}))
		case 1:
			h.Time = h.Time.Add(time.Duration(param) * time.Second)
		case 2:
			h.LastBlockID.Hash = hash(string([]byte{param}))
		case 3:
			h.NextValidatorsHash = fzValSetOf(fzByzantine).Hash()
		case 4:
			h.ProposerAddress = fzKeys[param%12].PubKey().Address()
		case 5:
			h.Height += int64(param%3) + 1
		case 6:
			h.Time = fzNow.Add(time.Duration(param) * time.Minute)
		case 7:
			h.DataHash = hash(string([]byte{param}))
		}
	}
	switch kind % 8 {
	case 0: // header changed, honest commit kept
		editHeader()
	case 1: // header changed and re-signed by the byzantine validator
		editHeader()
		lb.Commit = fzSign(h, lb.ValidatorSet, fzByzantine)
	case 2: // lunatic: attacker-controlled validator set
		editHeader()
		resign(fzValSetOf(fzByzantine))
	case 3: // another height's honest validator set
		editHeader()
		resign(fzValSet(int64(param % 16)))
	case 4: // corrupt one commit signature
		cs := &lb.Commit.Signatures[int(param)%len(lb.Commit.Signatures)]
		switch param % 3 {
		case 0:
			if len(cs.Signature) > 0 {
				cs.Signature[int(param)%len(cs.Signature)] ^= 1
			}
		case 1:
			*cs = types.NewCommitSigAbsent()
		case 2:
			lb.Commit.Signatures[0], *cs = *cs, lb.Commit.Signatures[0]
		}
	case 5: // proposer priorities are not covered by the validator set hash
		lb.ValidatorSet.Validators[int(param)%lb.ValidatorSet.Size()].ProposerPriority += int64(param) + 1
	case 6: // serve another height's honest block
		cp, err := fzCopy(fzHonest[1+int64(param)%fzHeights])
		if err != nil {
			panic(err)
		}
		p.blocks[height] = cp
	case 7:
		p.errs[height] = []error{
			provider.ErrNoResponse, provider.ErrLightBlockNotFound, provider.ErrHeightTooHigh,
			provider.ErrBadLightBlock{Reason: errors.New("fuzz")}, errors.New("fuzz"),
		}[param%5]
	}
}

// fzNewProvider builds a provider over a private copy of the honest chain
// with mutations applied, read as (height, kind, param) triples.
func fzNewProvider(t *testing.T, muts []byte, calls *atomic.Int64) (*fzProvider, bool) {
	p := &fzProvider{blocks: map[int64]*types.LightBlock{}, errs: map[int64]error{}, calls: calls}
	for h, lb := range fzHonest {
		cp, err := fzCopy(lb)
		require.NoError(t, err)
		p.blocks[h] = cp
	}
	tampered := false
	for i := 0; i+2 < len(muts) && i < 3*8; i += 3 {
		fzTamper(p, 1+int64(muts[i])%fzHeights, muts[i+1], muts[i+2])
		tampered = true
	}
	return p, tampered
}

// FuzzLightClientVerify runs the light client against a primary and witnesses
// that serve a deterministic chain with fuzzer-chosen tampering. The only key
// the attacker holds belongs to a validator with a quarter of the power, so
// the client must never trust a light block that differs from the honest
// chain, must terminate with bounded provider requests, and must verify the
// honest chain when nobody tampers.
func FuzzLightClientVerify(f *testing.F) {
	f.Add([]byte{0, 5, 0, 1}, []byte{}, []byte{}, []byte{})
	f.Add([]byte{1, 5, 1, 0}, []byte{}, []byte{}, []byte{})
	f.Add([]byte{0, 5, 2, 0}, []byte{3, 1, 0}, []byte{}, []byte{})
	f.Add([]byte{0, 5, 0, 0}, []byte{2, 2, 0}, []byte{}, []byte{})
	f.Add([]byte{2, 5, 0, 1}, []byte{}, []byte{5, 1, 0}, []byte{5, 5, 1})
	f.Add([]byte{0, 3, 1, 0}, []byte{2, 7, 0, 4, 4, 1}, []byte{4, 2, 9}, []byte{})
	f.Add([]byte{3, 1, 0, 4}, []byte{1, 0, 2}, []byte{}, []byte{})
	f.Add([]byte{0, 4, 4, 2}, []byte{0, 0, 0}, []byte{3, 6, 1}, []byte{})
	f.Fuzz(func(t *testing.T, cfg, primaryMuts, witness1Muts, witness2Muts []byte) {
		cfgAt := func(i int) byte {
			if i < len(cfg) {
				return cfg[i]
			}
			return 0
		}
		trustedHeight := 1 + int64(cfgAt(0)%3)
		target := 1 + int64(cfgAt(1)%fzHeights)
		mode := light.SkippingVerification(light.DefaultTrustLevel)
		if cfgAt(2)&1 != 0 {
			mode = light.SequentialVerification()
		}
		trustingPeriod := 24 * time.Hour
		if cfgAt(2)&4 != 0 {
			trustingPeriod = time.Duration(fzHeights-int(cfgAt(2)>>4)%fzHeights) * time.Minute
		}
		second := int64(cfgAt(3) % (fzHeights + 1))

		var calls atomic.Int64
		primary, tampered := fzNewProvider(t, primaryMuts, &calls)
		witness1, w1 := fzNewProvider(t, witness1Muts, &calls)
		witnesses := []provider.Provider{witness1}
		tampered = tampered || w1
		if cfgAt(2)&2 != 0 {
			witness2, w2 := fzNewProvider(t, witness2Muts, &calls)
			witnesses = append(witnesses, witness2)
			tampered = tampered || w2
		}
		honest := !tampered && cfgAt(2)&4 == 0

		trustedStore := dbs.New(dbm.NewMemDB(), fzChainID)
		c, err := light.NewClient(context.Background(), fzChainID,
			light.TrustOptions{Period: trustingPeriod, Height: trustedHeight, Hash: fzHonest[trustedHeight].Hash()},
			primary, witnesses, trustedStore,
			mode, light.MaxClockDrift(time.Millisecond), light.MaxBlockLag(0), light.MaxRetryAttempts(1))
		if honest {
			require.NoError(t, err, "honest providers rejected at init")
		}
		if err == nil {
			for _, h := range []int64{target, second} {
				if h == 0 {
					_, err = c.Update(context.Background(), fzNow)
				} else {
					var lb *types.LightBlock
					lb, err = c.VerifyLightBlockAtHeight(context.Background(), h, fzNow)
					if err == nil {
						require.Equal(t, fzHonest[h].Hash(), lb.Hash(), "verified a forged light block at %d", h)
					}
				}
				if honest {
					require.NoError(t, err, "honest chain rejected at height %d", h)
				}
			}
		}

		for h := int64(1); h <= fzHeights; h++ {
			lb, err := trustedStore.LightBlock(h)
			if err != nil {
				continue
			}
			require.Equal(t, fzHonest[h].Hash(), lb.Hash(), "trusted store holds a forged header at %d", h)
			require.Equal(t, fzHonest[h].ValidatorSet.Hash(), lb.ValidatorSet.Hash(),
				"trusted store holds a forged validator set at %d", h)
		}
		require.LessOrEqual(t, calls.Load(), int64(fzMaxCalls), "unbounded provider requests")
	})
}
