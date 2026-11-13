package conn

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gogotypes "github.com/cosmos/gogoproto/types"
	"github.com/oasisprotocol/curve25519-voi/primitives/merlin"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/curve25519"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/ed25519"
	cryptoenc "github.com/cometbft/cometbft/crypto/encoding"
	"github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/libs/protoio"
	pc "github.com/cometbft/cometbft/proto/tendermint/crypto"
	tmp2p "github.com/cometbft/cometbft/proto/tendermint/p2p"
)

const (
	fuzzMaxInput       = 64 << 10
	fuzzWatchdog       = 10 * time.Second
	sealedFrameSize    = totalFrameSize + aeadSizeOverhead
	fuzzMaxFrames      = 64
	fuzzMaxPackets     = 256
	fuzzMaxPacketBytes = 1100 // a bit above defaultMaxPacketMsgPayloadSize
)

var fuzzVictimKey = ed25519.GenPrivKeyFromSecret([]byte("p2p/conn fuzz victim"))

// fuzzEphKeys derives a deterministic X25519 key pair from seed.
func fuzzEphKeys(seed []byte) (pub, priv *[32]byte) {
	h := sha256.Sum256(seed)
	priv = new([32]byte)
	copy(priv[:], h[:])
	p, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		panic(err)
	}
	pub = new([32]byte)
	copy(pub[:], p)
	return pub, priv
}

// attackerSecretConn derives the session keys and challenge the way
// MakeSecretConnection does, so a scripted peer can speak the encrypted part
// of the handshake.
func attackerSecretConn(c io.ReadWriteCloser, ephPub, ephPriv, victimEphPub *[32]byte) (*SecretConnection, [32]byte, error) {
	var challenge [32]byte
	lo, hi := sort32(ephPub, victimEphPub)
	transcript := merlin.NewTranscript("TENDERMINT_SECRET_CONNECTION_TRANSCRIPT_HASH")
	transcript.AppendMessage(labelEphemeralLowerPublicKey, lo[:])
	transcript.AppendMessage(labelEphemeralUpperPublicKey, hi[:])
	dh, err := computeDHSecret(victimEphPub, ephPriv)
	if err != nil {
		return nil, challenge, err
	}
	transcript.AppendMessage(labelDHSecret, dh[:])
	recvSecret, sendSecret := deriveSecrets(dh, bytes.Equal(ephPub[:], lo[:]))
	transcript.ExtractBytes(challenge[:], labelSecretConnectionMac)
	sendAead, err := chacha20poly1305.New(sendSecret[:])
	if err != nil {
		return nil, challenge, err
	}
	recvAead, err := chacha20poly1305.New(recvSecret[:])
	if err != nil {
		return nil, challenge, err
	}
	return &SecretConnection{
		conn:            c,
		recvNonce:       new([aeadNonceSize]byte),
		sendNonce:       new([aeadNonceSize]byte),
		recvAead:        recvAead,
		sendAead:        sendAead,
		recvFrame:       make([]byte, totalFrameSize),
		recvSealedFrame: make([]byte, sealedFrameSize),
		sendFrame:       make([]byte, totalFrameSize),
		sendSealedFrame: make([]byte, sealedFrameSize),
	}, challenge, nil
}

// FuzzSecretConnectionHandshake runs a real MakeSecretConnection against a
// scripted peer whose handshake messages come from the fuzzer.
//
// flags bit 0: send rawPhase1 and rawPhase2 unencrypted instead of a real handshake.
// flags bit 1: after a valid key exchange, send rawPhase2 encrypted instead of an AuthSigMessage.
// flags bit 2: sign the real challenge with an ed25519 key derived from keySeed;
// sigMask is XORed into that signature.
// Otherwise the AuthSigMessage is built from keyType, pubKey and sigMask.
func FuzzSecretConnectionHandshake(f *testing.F) {
	f.Add(uint8(4), []byte("eph"), []byte{}, []byte("key"), uint8(0), []byte{}, []byte{})
	f.Add(uint8(4), []byte("eph"), []byte("trailing"), []byte("key"), uint8(0), []byte{}, []byte{1})
	f.Add(uint8(0), []byte("eph"), []byte{}, []byte{}, uint8(0), make([]byte, 32), make([]byte, 64))
	f.Add(uint8(0), []byte("eph"), []byte{}, []byte{}, uint8(1), make([]byte, 33), make([]byte, 64))
	f.Add(uint8(0), []byte("eph"), []byte{}, []byte{}, uint8(2), []byte{}, []byte{})
	f.Add(uint8(2), []byte("eph"), []byte{}, []byte{}, uint8(0), []byte{}, []byte{0x0a, 0x02, 0x0a, 0x00})
	f.Add(uint8(1), []byte{}, []byte{0x0a, 0x20}, []byte{}, uint8(0), []byte{}, []byte{})
	f.Add(uint8(1), []byte{}, []byte{0xff, 0xff, 0xff, 0x7f}, []byte{}, uint8(0), []byte{}, []byte{})

	f.Fuzz(func(t *testing.T, flags uint8, ephSeed, rawPhase1, keySeed []byte, keyType uint8, pubKey, sigMask []byte) {
		if len(rawPhase1)+len(pubKey)+len(sigMask) > fuzzMaxInput {
			return
		}
		var (
			raw          = flags&1 != 0
			rawEncrypted = flags&2 != 0
			honest       = flags&4 != 0
		)

		local, remote := makeKVStoreConnPair()
		var timedOut atomic.Bool
		watchdog := time.AfterFunc(fuzzWatchdog, func() {
			timedOut.Store(true)
			_ = local.Close()
			_ = remote.Close()
		})

		var (
			wg          sync.WaitGroup
			victimEph   = make(chan *[32]byte, 1)
			challenge   *[32]byte // set by the writer when it reaches phase 2
			sentKey     crypto.PubKey
			sentSig     []byte
			sentAuthMsg bool
		)
		wg.Add(2)
		go func() { // read the victim's ephemeral key, then drain
			defer wg.Done()
			var bv gogotypes.BytesValue
			if _, err := protoio.NewDelimitedReader(remote, 1024).ReadMsg(&bv); err == nil && len(bv.Value) == 32 {
				var k [32]byte
				copy(k[:], bv.Value)
				victimEph <- &k
			}
			close(victimEph)
			_, _ = io.Copy(io.Discard, remote)
		}()
		go func() {
			defer wg.Done()
			defer remote.PipeWriter.Close()
			if raw {
				// Skip empty writes: an io.Pipe delivers them as (0, nil)
				// reads, which a TCP socket never produces.
				for _, b := range [][]byte{rawPhase1, sigMask} {
					if len(b) > 0 {
						if _, err := remote.Write(b); err != nil {
							return
						}
					}
				}
				return
			}
			ephPub, ephPriv := fuzzEphKeys(ephSeed)
			value := append(append([]byte{}, ephPub[:]...), rawPhase1...)
			if _, err := protoio.NewDelimitedWriter(remote).WriteMsg(&gogotypes.BytesValue{Value: value}); err != nil {
				return
			}
			vEph, ok := <-victimEph
			if !ok {
				return
			}
			sc, ch, err := attackerSecretConn(remote, ephPub, ephPriv, vEph)
			if err != nil {
				return
			}
			challenge = &ch
			if rawEncrypted {
				_, _ = sc.Write(sigMask)
				return
			}
			var pbKey pc.PublicKey
			if honest {
				priv := ed25519.GenPrivKeyFromSecret(keySeed)
				sig, err := priv.Sign(ch[:])
				if err != nil {
					panic(err)
				}
				for i := 0; i < len(sig) && i < len(sigMask); i++ {
					sig[i] ^= sigMask[i]
				}
				sentKey, sentSig = priv.PubKey(), sig
				if pbKey, err = cryptoenc.PubKeyToProto(sentKey); err != nil {
					panic(err)
				}
			} else {
				switch keyType % 3 {
				case 0:
					pbKey.Sum = &pc.PublicKey_Ed25519{Ed25519: pubKey}
				case 1:
					pbKey.Sum = &pc.PublicKey_Secp256K1{Secp256K1: pubKey}
				}
				sentSig = sigMask
			}
			sentAuthMsg = true
			_, _ = protoio.NewDelimitedWriter(sc).WriteMsg(&tmp2p.AuthSigMessage{PubKey: pbKey, Sig: sentSig})
		}()

		sc, err := MakeSecretConnection(local, fuzzVictimKey)
		watchdog.Stop()
		_ = local.Close()
		wg.Wait()
		_ = remote.Close()

		if timedOut.Load() {
			t.Fatalf("handshake did not finish within %v", fuzzWatchdog)
		}
		if err != nil && strings.Contains(err.Error(), "panic in task") {
			t.Fatalf("handshake task panicked: %v", err)
		}
		if honest && !raw && !rawEncrypted && allZero(sigMask) && err != nil {
			t.Fatalf("honest handshake failed: %v", err)
		}
		if err != nil {
			return
		}
		if raw || rawEncrypted || !sentAuthMsg || challenge == nil {
			t.Fatalf("handshake succeeded without a signed challenge (raw=%v rawEncrypted=%v)", raw, rawEncrypted)
		}
		if !sc.RemotePubKey().VerifySignature(challenge[:], sentSig) {
			t.Fatalf("accepted remote key %v that did not sign the challenge", sc.RemotePubKey())
		}
		if sentKey != nil && !sc.RemotePubKey().Equals(sentKey) {
			t.Fatalf("remote key %v, sent %v", sc.RemotePubKey(), sentKey)
		}
	})
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// FuzzSecretConnectionRead completes a real handshake and then lets the
// fuzzer control the ciphertext stream that (*SecretConnection).Read
// consumes: validly sealed frames with arbitrary plaintext (including bad
// length prefixes), raw unauthenticated bytes, and regular Writes. A model
// computes the bytes Read must return and whether the stream must fail.
func FuzzSecretConnectionRead(f *testing.F) {
	f.Add([]byte{0, 0, 5, 'h', 'e', 'l', 'l', 'o'}, []byte{3})
	f.Add([]byte{1, 0, 0, 4, 1}, []byte{})                                   // length prefix > dataMaxSize
	f.Add([]byte{0, 0, 3, 'a', 'b', 'c', 2, 10, 0xde, 0xad}, []byte{1})      // ciphertext garbage
	f.Add([]byte{3, 0x08, 0x00, 7}, []byte{255, 0, 17})                      // multi-frame Write
	f.Add([]byte{2, 255}, []byte{})                                          // garbage only
	f.Add([]byte{0, 4, 0, 0xaa, 0, 0, 0}, []byte{200})                       // exactly dataMaxSize
	f.Add([]byte{0, 0, 1, 'x', 2, 0, 0, 0, 2, 'y', 'z'}, []byte{0, 0, 0, 0}) // garbage between frames

	f.Fuzz(func(t *testing.T, ops, readSizes []byte) {
		if len(ops) > fuzzMaxInput {
			return
		}
		attacker, victim := makeSecretConnPair(t)
		attackerConn := attacker.conn.(kvstoreConn)

		// Build the stream and the expected outcome.
		var (
			stream   [][]byte // writes, in order
			expected []byte
			broken   bool
			wantLen  bool // a frame with an oversize length prefix decrypts first
			wantFail bool
			take     = func(n int) []byte {
				if n > len(ops) {
					n = len(ops)
				}
				b := ops[:n]
				ops = ops[n:]
				return b
			}
		)
		for frames := 0; len(ops) > 0 && frames < fuzzMaxFrames; frames++ {
			op := ops[0]
			ops = ops[1:]
			switch op % 4 {
			case 0, 1: // one sealed frame with a fuzz-chosen plaintext
				frame := make([]byte, totalFrameSize)
				var chunkLen uint32
				if op%4 == 0 {
					// Copy before appending: take returns a subslice of
					// ops, so appending in place would overwrite the input.
					hdr := append(append([]byte{}, take(2)...), 0, 0)
					chunkLen = uint32(binary.BigEndian.Uint16(hdr)) % (dataMaxSize + 1)
				} else {
					hdr := append(append([]byte{}, take(4)...), 0, 0, 0, 0)
					chunkLen = binary.LittleEndian.Uint32(hdr)
				}
				binary.LittleEndian.PutUint32(frame, chunkLen)
				copy(frame[dataLenSize:], take(int(min(chunkLen, dataMaxSize))))
				sealed := attacker.sendAead.Seal(nil, attacker.sendNonce[:], frame, nil)
				incrNonce(attacker.sendNonce)
				stream = append(stream, sealed)
				if !broken {
					if chunkLen > dataMaxSize {
						broken, wantLen = true, true
					} else {
						expected = append(expected, frame[dataLenSize:dataLenSize+chunkLen]...)
					}
				}
			case 2: // unauthenticated bytes
				var n int
				if b := take(1); len(b) == 1 {
					n = 1 + int(b[0])*8
				}
				garbage := append([]byte{}, take(n)...)
				for len(garbage) < n {
					garbage = append(garbage, op)
				}
				if n > 0 {
					stream = append(stream, garbage)
					if !broken {
						broken, wantFail = true, true
					}
				}
			case 3: // regular Write, possibly spanning several frames
				hdr := append(append([]byte{}, take(2)...), 0, 0)
				n := int(binary.BigEndian.Uint16(hdr)) % (3 * dataMaxSize)
				data := make([]byte, n)
				for i := range data {
					data[i] = byte(i) ^ op
				}
				var buf bytes.Buffer
				attacker.conn = &writerCloser{&buf}
				if _, err := attacker.Write(data); err != nil {
					t.Fatal(err)
				}
				attacker.conn = attackerConn
				if buf.Len() > 0 {
					stream = append(stream, buf.Bytes())
				}
				if !broken {
					expected = append(expected, data...)
				}
			}
		}

		// A sealed frame with a zero length prefix legitimately reads as
		// (0, nil); anything beyond one such read per sealed frame in the
		// stream means Read is spinning without consuming input.
		var streamLen int
		for _, w := range stream {
			streamLen += len(w)
		}
		maxEmptyReads := streamLen / sealedFrameSize

		done := make(chan struct{})
		go func() {
			defer close(done)
			defer attackerConn.PipeWriter.Close()
			for _, w := range stream {
				if _, err := attackerConn.Write(w); err != nil {
					return
				}
			}
		}()

		// The endpoints are io.Pipes, which have no read deadline (and
		// SecretConnection.SetReadDeadline requires a net.Conn), so a
		// watchdog closes them to unblock a stuck Read. Cleanup closes them
		// even if the loop below fails, which also unblocks the writer.
		victimConn := victim.conn.(kvstoreConn)
		var timedOut atomic.Bool
		watchdog := time.AfterFunc(fuzzWatchdog, func() {
			timedOut.Store(true)
			_ = victimConn.Close()
			_ = attackerConn.Close()
		})
		t.Cleanup(func() {
			watchdog.Stop()
			_ = victimConn.Close()
			_ = attackerConn.Close()
			<-done
		})

		var (
			got        []byte
			readErr    error
			emptyReads int
		)
		for i := 0; ; i++ {
			size := dataMaxSize
			if len(readSizes) > 0 {
				size = 1 + int(readSizes[i%len(readSizes)])*8
			}
			buf := make([]byte, size)
			n, err := victim.Read(buf)
			if n < 0 || n > len(buf) {
				t.Fatalf("Read returned n=%d for a %d byte buffer", n, len(buf))
			}
			got = append(got, buf[:n]...)
			if err != nil {
				readErr = err
				break
			}
			if n == 0 {
				if emptyReads++; emptyReads > maxEmptyReads {
					t.Fatalf("Read returned (0, nil) %d times for %d sealed frames", emptyReads, maxEmptyReads)
				}
			}
			if len(got) > len(expected) {
				t.Fatalf("read %d bytes, model expects at most %d", len(got), len(expected))
			}
		}
		watchdog.Stop()
		_ = victim.Close()
		<-done
		_ = attacker.Close()

		if timedOut.Load() {
			t.Fatalf("Read loop did not finish within %v (read %d of %d bytes)", fuzzWatchdog, len(got), len(expected))
		}
		if !bytes.Equal(got, expected) {
			t.Fatalf("read %d bytes, expected %d (err=%v)", len(got), len(expected), readErr)
		}
		switch {
		case wantLen:
			if readErr == nil || !strings.Contains(readErr.Error(), "chunkLength") {
				t.Fatalf("expected chunkLength error, got %v", readErr)
			}
		case wantFail:
			if errors.Is(readErr, io.EOF) {
				t.Fatalf("expected failure after unauthenticated bytes, got %v", readErr)
			}
		default:
			if !errors.Is(readErr, io.EOF) {
				t.Fatalf("expected clean EOF, got %v", readErr)
			}
		}
	})
}

type writerCloser struct{ io.Writer }

func (writerCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (writerCloser) Close() error             { return nil }

// halfPipe is a net.Conn made of two io.Pipes so each side can close its
// write direction independently (net.Pipe cannot half-close).
type halfPipe struct {
	*io.PipeReader
	*io.PipeWriter
}

func (p halfPipe) Close() error {
	_ = p.PipeWriter.Close()
	return p.PipeReader.Close()
}
func (halfPipe) LocalAddr() net.Addr              { return pipeAddr{} }
func (halfPipe) RemoteAddr() net.Addr             { return pipeAddr{} }
func (halfPipe) SetDeadline(time.Time) error      { return nil }
func (halfPipe) SetReadDeadline(time.Time) error  { return nil }
func (halfPipe) SetWriteDeadline(time.Time) error { return nil }

type pipeAddr struct{}

func (pipeAddr) Network() string { return "pipe" }
func (pipeAddr) String() string  { return "pipe" }

func newHalfPipe() (a, b halfPipe) {
	ar, bw := io.Pipe()
	br, aw := io.Pipe()
	return halfPipe{ar, aw}, halfPipe{br, bw}
}

type fuzzRecvMsg struct {
	ch   byte
	data []byte
}

// FuzzMConnectionRecv drives a running MConnection's recvRoutine with packets
// written by the peer. In structured mode a model of packet framing and
// reassembly predicts every message onReceive must see and whether the
// connection must end with a clean EOF or an error.
func FuzzMConnectionRecv(f *testing.F) {
	f.Add(false, uint16(100), []byte{2 | 0x80, 0, 0, 5})                // one message on ch 1
	f.Add(false, uint16(100), []byte{2, 0, 0, 60, 2 | 0x80, 0, 0, 60})  // reassembly over the cap
	f.Add(false, uint16(4095), []byte{0, 1, 2 | 0x80, 1, 4, 0})         // ping, pong, msg on ch 2
	f.Add(false, uint16(10), []byte{2 | 0x80, 2, 0, 1})                 // unknown channel
	f.Add(false, uint16(10), []byte{4})                                 // empty packet
	f.Add(false, uint16(2000), []byte{2 | 0x80, 0, 4, 0x4c})            // payload above max packet size
	f.Add(false, uint16(10), []byte{2 | 0x80, 5, 0, 1})                 // negative channel
	f.Add(true, uint16(10), []byte{0x05, 0x1a, 0x03, 0x08, 0x01, 0x10}) // raw
	f.Add(true, uint16(10), []byte{0xff, 0xff, 0xff, 0xff, 0x0f})

	chIDs := []int32{1, 2, 3, 0, 255, -1, 256, 1 << 30}

	f.Fuzz(func(t *testing.T, raw bool, capSel uint16, data []byte) {
		if len(data) > fuzzMaxInput {
			return
		}
		recvCaps := map[byte]int{0x01: 1 + int(capSel)%4096, 0x02: 2048}
		cfg := DefaultMConnConfig()
		cfg.SendRate, cfg.RecvRate = 0, 0 // unthrottled
		cfg.PingInterval, cfg.PongTimeout = time.Hour, 30*time.Minute
		chDescs := []*ChannelDescriptor{
			{ID: 0x01, Priority: 1, RecvMessageCapacity: recvCaps[0x01], RecvBufferCapacity: 16},
			{ID: 0x02, Priority: 1, RecvMessageCapacity: recvCaps[0x02]},
		}

		var (
			received  []fuzzRecvMsg
			errCalls  atomic.Int32
			onErrArg  interface{}
			errCalled = make(chan struct{})
		)
		local, remote := newHalfPipe()
		mconn := NewMConnectionWithConfig(local, chDescs,
			func(chID byte, msg []byte) {
				received = append(received, fuzzRecvMsg{chID, append([]byte{}, msg...)})
			},
			func(r interface{}) {
				if errCalls.Add(1) == 1 {
					onErrArg = r
					close(errCalled)
				}
			}, cfg)
		mconn.SetLogger(log.NewNopLogger())

		// Build the stream and, in structured mode, the expected result.
		var (
			stream   []byte
			expected []fuzzRecvMsg
			wantErr  bool
			pending  = map[byte][]byte{}
		)
		if raw {
			stream = data
		} else {
			for n := 0; len(data) > 0 && n < fuzzMaxPackets; n++ {
				op := data[0]
				data = data[1:]
				var pkt *tmp2p.Packet
				switch op & 0x7f % 5 {
				case 0:
					pkt = mustWrapPacket(&tmp2p.PacketPing{})
				case 1:
					pkt = mustWrapPacket(&tmp2p.PacketPong{})
				case 2, 3:
					args := append(append([]byte{}, data[:min(3, len(data))]...), 0, 0, 0)
					data = data[min(3, len(data)):]
					payload := make([]byte, int(binary.BigEndian.Uint16(args[1:3]))%fuzzMaxPacketBytes)
					for i := range payload {
						payload[i] = byte(i) ^ op
					}
					pkt = mustWrapPacket(&tmp2p.PacketMsg{
						ChannelID: chIDs[int(args[0])%len(chIDs)],
						EOF:       op&0x80 != 0,
						Data:      payload,
					})
				default:
					pkt = &tmp2p.Packet{}
				}
				bz, err := protoio.MarshalDelimited(pkt)
				if err != nil {
					t.Fatal(err)
				}
				stream = append(stream, bz...)
				if wantErr {
					continue
				}
				// Model of recvRoutine.
				if pkt.Size() > mconn._maxPacketMsgSize {
					wantErr = true
					continue
				}
				msg, ok := pkt.Sum.(*tmp2p.Packet_PacketMsg)
				if !ok {
					wantErr = pkt.Sum == nil
					continue
				}
				id := msg.PacketMsg.ChannelID
				if id < 0 || id > 255 || recvCaps[byte(id)] == 0 {
					wantErr = true
					continue
				}
				buf := append(pending[byte(id)], msg.PacketMsg.Data...)
				if len(buf) > recvCaps[byte(id)] {
					wantErr = true
					continue
				}
				pending[byte(id)] = buf
				if msg.PacketMsg.EOF {
					expected = append(expected, fuzzRecvMsg{byte(id), append([]byte{}, buf...)})
					pending[byte(id)] = nil
				}
			}
		}

		if err := mconn.Start(); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			if len(stream) > 0 { // see FuzzSecretConnectionHandshake
				_, _ = remote.Write(stream)
			}
			_ = remote.PipeWriter.Close()
		}()
		go func() { // drain pongs
			defer wg.Done()
			_, _ = io.Copy(io.Discard, remote)
		}()

		select {
		case <-errCalled:
		case <-time.After(fuzzWatchdog):
			t.Fatalf("recvRoutine did not finish within %v", fuzzWatchdog)
		}
		<-mconn.doneSendRoutine
		_ = remote.Close()
		wg.Wait()

		if errCalls.Load() != 1 {
			t.Fatalf("onError called %d times", errCalls.Load())
		}
		if err, ok := onErrArg.(error); ok && strings.HasPrefix(err.Error(), "recovered from panic") {
			t.Fatalf("recvRoutine panicked: %v", err)
		}
		for _, m := range received {
			if c := recvCaps[m.ch]; c == 0 || len(m.data) > c {
				t.Fatalf("onReceive got %d bytes on channel %#x (capacity %d)", len(m.data), m.ch, c)
			}
		}
		if raw {
			return
		}
		if len(received) != len(expected) {
			t.Fatalf("received %d messages, expected %d (onError=%v)", len(received), len(expected), onErrArg)
		}
		for i := range expected {
			if received[i].ch != expected[i].ch || !bytes.Equal(received[i].data, expected[i].data) {
				t.Fatalf("message %d: got ch %#x len %d, expected ch %#x len %d", i,
					received[i].ch, len(received[i].data), expected[i].ch, len(expected[i].data))
			}
		}
		if isEOF := onErrArg == io.EOF; isEOF == wantErr {
			t.Fatalf("onError(%v), model expects error=%v", onErrArg, wantErr)
		}
	})
}
