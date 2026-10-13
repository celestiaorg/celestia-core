package consensus

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"

	// "sync"
	"testing"
	"time"

	"github.com/klauspost/compress/s2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/consensus/types"
	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/libs/autofile"
	"github.com/cometbft/cometbft/libs/log"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	cmttypes "github.com/cometbft/cometbft/types"
	cmttime "github.com/cometbft/cometbft/types/time"
)

const (
	walTestFlushInterval = time.Duration(100) * time.Millisecond
)

func TestWALTruncate(t *testing.T) {
	walDir, err := os.MkdirTemp("", "wal")
	require.NoError(t, err)
	defer os.RemoveAll(walDir)

	walFile := filepath.Join(walDir, "wal")

	// this magic number 4K can truncate the content when RotateFile.
	// defaultHeadSizeLimit(10M) is hard to simulate.
	// this magic number 1 * time.Millisecond make RotateFile check frequently.
	// defaultGroupCheckDuration(5s) is hard to simulate.
	wal, err := NewWAL(walFile,
		autofile.GroupHeadSizeLimit(4096),
		autofile.GroupCheckDuration(1*time.Millisecond),
	)
	require.NoError(t, err)
	wal.SetLogger(log.TestingLogger())
	err = wal.Start()
	require.NoError(t, err)
	defer func() {
		if err := wal.Stop(); err != nil {
			t.Error(err)
		}
		// wait for the wal to finish shutting down so we
		// can safely remove the directory
		wal.Wait()
	}()

	// 60 block's size nearly 70K, greater than group's headBuf size(4096 * 10),
	// when headBuf is full, truncate content will Flush to the file. at this
	// time, RotateFile is called, truncate content exist in each file.
	err = WALGenerateNBlocks(t, wal.Group(), 60, getConfig(t))
	require.NoError(t, err)

	time.Sleep(1 * time.Millisecond) // wait groupCheckDuration, make sure RotateFile run

	if err := wal.FlushAndSync(); err != nil {
		t.Error(err)
	}

	h := int64(50)
	gr, found, err := wal.SearchForEndHeight(h, &WALSearchOptions{})
	assert.NoError(t, err, "expected not to err on height %d", h)
	assert.True(t, found, "expected to find end height for %d", h)
	assert.NotNil(t, gr)
	defer gr.Close()

	dec := NewWALDecoder(gr)
	msg, err := dec.Decode()
	assert.NoError(t, err, "expected to decode a message")
	rs, ok := msg.Msg.(cmttypes.EventDataRoundState)
	assert.True(t, ok, "expected message of type EventDataRoundState")
	assert.Equal(t, rs.Height, h+1, "wrong height")
}

func TestWALEncoderDecoder(t *testing.T) {
	now := cmttime.Now()
	msgs := []TimedWALMessage{
		{Time: now, Msg: EndHeightMessage{0}},
		{Time: now, Msg: timeoutInfo{Duration: time.Second, Height: 1, Round: 1, Step: types.RoundStepPropose}},
		{Time: now, Msg: cmttypes.EventDataRoundState{Height: 1, Round: 1, Step: ""}},
	}

	b := new(bytes.Buffer)

	for _, msg := range msgs {
		msg := msg

		b.Reset()

		enc := NewWALEncoder(b)
		err := enc.Encode(&msg)
		require.NoError(t, err)
		require.Zero(t, binary.BigEndian.Uint32(b.Bytes()[4:8])&walCompressionFlag)

		dec := NewWALDecoder(b)
		decoded, err := dec.Decode()
		require.NoError(t, err)
		assert.Equal(t, msg.Time.UTC(), decoded.Time)
		assert.Equal(t, msg.Msg, decoded.Msg)
	}
}

func TestWALEncoderCompression(t *testing.T) {
	blockPart := func(data []byte) TimedWALMessage {
		return TimedWALMessage{Time: time.Unix(1, 0).UTC(), Msg: msgInfo{Msg: &BlockPartMessage{
			Height: 1,
			Part:   &cmttypes.Part{Bytes: data, Proof: merkle.Proof{Total: 1, LeafHash: nBytes(32)}},
		}}}
	}
	size := int(cmttypes.BlockPartSizeBytes)
	tests := []struct {
		name           string
		enc            func(io.Writer) *WALEncoder
		msg            TimedWALMessage
		wantCompressed bool
	}{
		{"compressible", NewWALEncoder, blockPart(bytes.Repeat([]byte{0xAB}, size)), true},
		{"incompressible", NewWALEncoder, blockPart(nBytes(size)), false},
		{"small", NewWALEncoder, TimedWALMessage{Time: time.Unix(1, 0).UTC(), Msg: EndHeightMessage{1}}, false},
		{"disabled", NewWALEncoderWithoutCompression, blockPart(bytes.Repeat([]byte{0xAB}, size)), false},
	}

	// All records go into one stream so the decoder also sees mixed formats
	// and reuses its buffers across them.
	var stream bytes.Buffer
	for _, tc := range tests {
		start := stream.Len()
		require.NoError(t, tc.enc(&stream).Encode(&tc.msg), tc.name)
		lengthField := binary.BigEndian.Uint32(stream.Bytes()[start+4 : start+8])
		assert.Equal(t, tc.wantCompressed, lengthField&walCompressionFlag != 0, tc.name)
	}

	dec := NewWALDecoder(&stream)
	decoded := make([]*TimedWALMessage, len(tests))
	for i, tc := range tests {
		var err error
		decoded[i], err = dec.Decode()
		require.NoError(t, err, tc.name)
	}
	_, err := dec.Decode()
	require.ErrorIs(t, err, io.EOF)

	// Checked after all reads so buffer reuse can't mutate earlier results.
	for i, tc := range tests {
		assert.Equal(t, tc.msg, *decoded[i], tc.name)
	}
}

func TestWALDecoderRejectsCorruptCompressedData(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"bad length header", []byte{0xFF}, "compressed length"},
		{"bad body", []byte{0x01, 0x00}, "failed to decompress data"},
		{"oversized", s2.EncodeSnappy(nil, make([]byte, maxMsgSizeBytes+1)), "decoded length"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			record := make([]byte, 8+len(tc.payload))
			binary.BigEndian.PutUint32(record[0:4], crc32.Checksum(tc.payload, crc32c))
			binary.BigEndian.PutUint32(record[4:8], uint32(len(tc.payload))|walCompressionFlag)
			copy(record[8:], tc.payload)

			_, err := NewWALDecoder(bytes.NewReader(record)).Decode()
			require.True(t, IsDataCorruptionError(err), err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// BenchmarkWAL32MiBBlock writes one height's WAL traffic for a 32 MiB PFB block
// with 100 validators: proposal, all block parts, and a prevote and precommit
// from each validator. Signed own messages are synced like in State.
func BenchmarkWAL32MiBBlock(b *testing.B) {
	const validators = 100
	for _, compressible := range []bool{true, false} {
		b.Run(fmt.Sprintf("compressible=%t", compressible), func(b *testing.B) {
			txs := make([]cmttypes.Tx, 32)
			for i := range txs {
				data := bytes.Repeat([]byte{byte(i)}, 1<<20)
				if !compressible {
					data = nBytes(len(data))
				}
				blob := &cmtproto.Blob{NamespaceId: bytes.Repeat([]byte{byte(i)}, 28), Data: data}
				tx, err := cmttypes.MarshalBlobTx([]byte("msg-pay-for-blobs"), blob)
				require.NoError(b, err)
				txs[i] = tx
			}
			block := cmttypes.MakeBlock(1, cmttypes.MakeData(txs), &cmttypes.Commit{}, nil)
			parts, err := block.MakePartSet(cmttypes.BlockPartSizeBytes)
			require.NoError(b, err)
			blockID := cmttypes.BlockID{Hash: block.Hash(), PartSetHeader: parts.Header()}

			vote := func(typ cmtproto.SignedMsgType) msgInfo {
				return msgInfo{Msg: &VoteMessage{Vote: &cmttypes.Vote{
					Type: typ, Height: 1, BlockID: blockID, Timestamp: time.Unix(1, 0),
					ValidatorAddress: nBytes(20), Signature: nBytes(64),
				}}, PeerID: "peer"}
			}
			proposal := msgInfo{Msg: &ProposalMessage{Proposal: &cmttypes.Proposal{
				Type: cmtproto.ProposalType, Height: 1, POLRound: -1, BlockID: blockID,
				Timestamp: time.Unix(1, 0), Signature: nBytes(64),
			}}}
			prevote, precommit := vote(cmtproto.PrevoteType), vote(cmtproto.PrecommitType)

			wal := setupBenchmarkWAL(b)
			write := func(msg WALMessage, sync bool) {
				write := wal.Write
				if sync {
					write = wal.WriteSync
				}
				if err := write(msg); err != nil {
					b.Fatal(err)
				}
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				write(proposal, true)
				for j := 0; j < int(parts.Total()); j++ {
					write(msgInfo{Msg: &BlockPartMessage{Height: 1, Part: parts.GetPart(j)}}, false)
				}
				for _, v := range []msgInfo{prevote, precommit} {
					write(v, true)
					for j := 1; j < validators; j++ {
						write(v, false)
					}
				}
				write(EndHeightMessage{Height: 1}, true)
			}
		})
	}
}

func TestWALWrite(t *testing.T) {
	walDir, err := os.MkdirTemp("", "wal")
	require.NoError(t, err)
	defer os.RemoveAll(walDir)
	walFile := filepath.Join(walDir, "wal")

	wal, err := NewWAL(walFile)
	require.NoError(t, err)
	err = wal.Start()
	require.NoError(t, err)
	defer func() {
		if err := wal.Stop(); err != nil {
			t.Error(err)
		}
		// wait for the wal to finish shutting down so we
		// can safely remove the directory
		wal.Wait()
	}()

	// 1) Write returns an error if msg is too big
	msg := &BlockPartMessage{
		Height: 1,
		Round:  1,
		Part: &cmttypes.Part{
			Index: 1,
			Bytes: make([]byte, 1),
			Proof: merkle.Proof{
				Total:    1,
				Index:    1,
				LeafHash: make([]byte, maxMsgSizeBytes-30),
			},
		},
	}

	err = wal.Write(msgInfo{
		Msg: msg,
	})
	if assert.Error(t, err) {
		assert.Contains(t, err.Error(), "msg is too big")
	}
}

func TestWALSearchForEndHeight(t *testing.T) {
	walBody, err := WALWithNBlocks(t, 6, getConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	walFile := tempWALWithData(walBody)

	wal, err := NewWAL(walFile)
	require.NoError(t, err)
	wal.SetLogger(log.TestingLogger())

	h := int64(3)
	gr, found, err := wal.SearchForEndHeight(h, &WALSearchOptions{})
	assert.NoError(t, err, "expected not to err on height %d", h)
	assert.True(t, found, "expected to find end height for %d", h)
	assert.NotNil(t, gr)
	defer gr.Close()

	dec := NewWALDecoder(gr)
	msg, err := dec.Decode()
	assert.NoError(t, err, "expected to decode a message")
	rs, ok := msg.Msg.(cmttypes.EventDataRoundState)
	assert.True(t, ok, "expected message of type EventDataRoundState")
	assert.Equal(t, rs.Height, h+1, "wrong height")
}

func TestWALPeriodicSync(t *testing.T) {
	walDir, err := os.MkdirTemp("", "wal")
	require.NoError(t, err)
	defer os.RemoveAll(walDir)

	walFile := filepath.Join(walDir, "wal")
	wal, err := NewWAL(walFile, autofile.GroupCheckDuration(1*time.Millisecond))
	require.NoError(t, err)

	wal.SetFlushInterval(walTestFlushInterval)
	wal.SetLogger(log.TestingLogger())

	// Generate some data
	err = WALGenerateNBlocks(t, wal.Group(), 5, getConfig(t))
	require.NoError(t, err)

	// We should have data in the buffer now
	assert.NotZero(t, wal.Group().Buffered())

	require.NoError(t, wal.Start())
	defer func() {
		if err := wal.Stop(); err != nil {
			t.Error(err)
		}
		wal.Wait()
	}()

	time.Sleep(walTestFlushInterval + (10 * time.Millisecond))

	// The data should have been flushed by the periodic sync
	assert.Zero(t, wal.Group().Buffered())

	h := int64(4)
	gr, found, err := wal.SearchForEndHeight(h, &WALSearchOptions{})
	assert.NoError(t, err, "expected not to err on height %d", h)
	assert.True(t, found, "expected to find end height for %d", h)
	assert.NotNil(t, gr)
	if gr != nil {
		gr.Close()
	}
}

/*
var initOnce sync.Once

func registerInterfacesOnce() {
	initOnce.Do(func() {
		var _ = wire.RegisterInterface(
			struct{ WALMessage }{},
			wire.ConcreteType{[]byte{}, 0x10},
		)
	})
}
*/

func nBytes(n int) []byte {
	buf := make([]byte, n)
	n, _ = rand.Read(buf)
	return buf[:n]
}

func benchmarkWalDecode(b *testing.B, n int) {
	// registerInterfacesOnce()

	buf := new(bytes.Buffer)
	enc := NewWALEncoder(buf)
	msg := msgInfo{Msg: &BlockPartMessage{Height: 1, Round: 0, Part: &cmttypes.Part{
		Index: 0,
		Bytes: nBytes(n),
		Proof: merkle.Proof{Total: 1, Index: 0, LeafHash: nBytes(32)},
	}}}
	if err := enc.Encode(&TimedWALMessage{Msg: msg, Time: time.Now().Round(time.Second).UTC()}); err != nil {
		b.Fatal(err)
	}
	encoded := buf.Bytes()
	r := bytes.NewReader(encoded)
	dec := NewWALDecoder(r)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Reset(encoded)
		if _, err := dec.Decode(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
}

func BenchmarkWalDecode512B(b *testing.B) {
	benchmarkWalDecode(b, 512)
}

func BenchmarkWalDecode10KB(b *testing.B) {
	benchmarkWalDecode(b, 10*1024)
}
func BenchmarkWalDecode100KB(b *testing.B) {
	benchmarkWalDecode(b, 100*1024)
}
func BenchmarkWalDecode1MB(b *testing.B) {
	benchmarkWalDecode(b, 1024*1024)
}
func BenchmarkWalDecode10MB(b *testing.B) {
	benchmarkWalDecode(b, 10*1024*1024)
}
func BenchmarkWalDecode100MB(b *testing.B) {
	benchmarkWalDecode(b, 100*1024*1024)
}
func BenchmarkWalDecode1GB(b *testing.B) {
	benchmarkWalDecode(b, 1024*1024*1024)
}

func setupBenchmarkWAL(b *testing.B) *BaseWAL {
	b.Helper()
	walDir := b.TempDir()
	walFile := filepath.Join(walDir, "wal")
	wal, err := NewWAL(walFile)
	require.NoError(b, err)
	wal.SetLogger(log.TestingLogger())
	err = wal.Start()
	require.NoError(b, err)
	b.Cleanup(func() {
		if err := wal.Stop(); err != nil {
			b.Error(err)
		}
		wal.Wait()
	})
	return wal
}

func BenchmarkWALWrite(b *testing.B) {
	wal := setupBenchmarkWAL(b)
	msg := msgInfo{Msg: &BlockPartMessage{Height: 1, Round: 0, Part: &cmttypes.Part{
		Index: 0,
		Bytes: nBytes(512),
		Proof: merkle.Proof{Total: 1, Index: 0, LeafHash: nBytes(32)},
	}}}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := wal.Write(msg); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
}

func BenchmarkWALWriteSync(b *testing.B) {
	wal := setupBenchmarkWAL(b)
	msg := msgInfo{Msg: &BlockPartMessage{Height: 1, Round: 0, Part: &cmttypes.Part{
		Index: 0,
		Bytes: nBytes(512),
		Proof: merkle.Proof{Total: 1, Index: 0, LeafHash: nBytes(32)},
	}}}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := wal.WriteSync(msg); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
}

// BenchmarkWALFlushAndSyncClean measures FlushAndSync with no pending data.
func BenchmarkWALFlushAndSyncClean(b *testing.B) {
	wal := setupBenchmarkWAL(b)

	if err := wal.FlushAndSync(); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := wal.FlushAndSync(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
}

// BenchmarkWALRoundSimulation simulates a proposer's round with N block parts,
// comparing the old approach (WriteSync for all) vs new (Write for block parts).
func BenchmarkWALRoundSimulation(b *testing.B) {
	const numBlockParts = 50

	proposal := msgInfo{Msg: &ProposalMessage{Proposal: &cmttypes.Proposal{}}}
	vote := msgInfo{Msg: &VoteMessage{Vote: &cmttypes.Vote{}}}
	blockPart := msgInfo{Msg: &BlockPartMessage{Height: 1, Round: 0, Part: &cmttypes.Part{
		Index: 0,
		Bytes: nBytes(512),
		Proof: merkle.Proof{Total: 1, Index: 0, LeafHash: nBytes(32)},
	}}}

	b.Run("AllWriteSync", func(b *testing.B) {
		wal := setupBenchmarkWAL(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := wal.WriteSync(proposal); err != nil {
				b.Fatal(err)
			}
			for j := 0; j < numBlockParts; j++ {
				if err := wal.WriteSync(blockPart); err != nil {
					b.Fatal(err)
				}
			}
			if err := wal.WriteSync(vote); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("SelectiveFsync", func(b *testing.B) {
		wal := setupBenchmarkWAL(b)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if err := wal.WriteSync(proposal); err != nil {
				b.Fatal(err)
			}
			for j := 0; j < numBlockParts; j++ {
				if err := wal.Write(blockPart); err != nil {
					b.Fatal(err)
				}
			}
			if err := wal.WriteSync(vote); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkWalDecode50KB(b *testing.B) {
	benchmarkWalDecode(b, 50*1024)
}

// namedWALMessage pairs a human-readable name with a WALMessage for benchmarks.
type namedWALMessage struct {
	name string
	msg  WALMessage
}

// realWALMessages returns a set of realistic WAL message types used in
// consensus: a vote, a proposal, a block part, and a timeout.
func realWALMessages() []namedWALMessage {
	vote := &cmttypes.Vote{
		Type:   cmtproto.PrevoteType,
		Height: 100,
		Round:  0,
		BlockID: cmttypes.BlockID{
			Hash: nBytes(32),
			PartSetHeader: cmttypes.PartSetHeader{
				Total: 10,
				Hash:  nBytes(32),
			},
		},
		ValidatorAddress: nBytes(20),
		ValidatorIndex:   0,
		Signature:        nBytes(64),
	}
	proposal := &cmttypes.Proposal{
		Type:   cmtproto.ProposalType,
		Height: 100,
		Round:  0,
		BlockID: cmttypes.BlockID{
			Hash:          nBytes(32),
			PartSetHeader: cmttypes.PartSetHeader{Total: 10, Hash: nBytes(32)},
		},
		Signature: nBytes(64),
	}
	blockPart := &cmttypes.Part{
		Index: 0,
		Bytes: nBytes(512),
		Proof: merkle.Proof{Total: 1, Index: 0, LeafHash: nBytes(32)},
	}
	return []namedWALMessage{
		{"Vote", msgInfo{Msg: &VoteMessage{Vote: vote}}},
		{"Proposal", msgInfo{Msg: &ProposalMessage{Proposal: proposal}}},
		{"BlockPart", msgInfo{Msg: &BlockPartMessage{Height: 100, Round: 0, Part: blockPart}}},
		{"Timeout", timeoutInfo{Duration: time.Second, Height: 100, Round: 0, Step: types.RoundStepPrevote}},
	}
}

// BenchmarkWALEncodeRealMessages benchmarks Encode with realistic consensus
// message types, exercising WALToProto and proto serialization paths.
func BenchmarkWALEncodeRealMessages(b *testing.B) {
	for _, nm := range realWALMessages() {
		b.Run(nm.name, func(b *testing.B) {
			buf := new(bytes.Buffer)
			enc := NewWALEncoder(buf)
			timed := &TimedWALMessage{Time: time.Now(), Msg: nm.msg}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := enc.Encode(timed); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkWALDecodeRealMessages benchmarks Decode with realistic consensus
// message types, exercising WALFromProto and proto deserialization paths.
func BenchmarkWALDecodeRealMessages(b *testing.B) {
	for _, nm := range realWALMessages() {
		b.Run(nm.name, func(b *testing.B) {
			var encoded bytes.Buffer
			enc := NewWALEncoder(&encoded)
			if err := enc.Encode(&TimedWALMessage{Time: time.Now(), Msg: nm.msg}); err != nil {
				b.Fatal(err)
			}
			raw := encoded.Bytes()

			buf := bytes.NewReader(raw)
			dec := NewWALDecoder(buf)
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				buf.Reset(raw)
				if _, err := dec.Decode(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkWALRoundtripRealMessages benchmarks a full encode→decode cycle.
func BenchmarkWALRoundtripRealMessages(b *testing.B) {
	for _, nm := range realWALMessages() {
		b.Run(nm.name, func(b *testing.B) {
			buf := new(bytes.Buffer)
			enc := NewWALEncoder(buf)
			timed := &TimedWALMessage{Time: time.Now(), Msg: nm.msg}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				buf.Reset()
				if err := enc.Encode(timed); err != nil {
					b.Fatal(err)
				}
				dec := NewWALDecoder(buf)
				if _, err := dec.Decode(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
