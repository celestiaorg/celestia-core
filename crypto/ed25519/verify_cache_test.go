package ed25519

import (
	"crypto/sha256"
	"encoding/binary"
	"math/rand"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	stded25519 "golang.org/x/crypto/ed25519"
)

func setVerifyCache(t *testing.T, on bool) {
	t.Helper()
	prev := verifyCacheEnabled
	verifyCacheEnabled = on
	t.Cleanup(func() { verifyCacheEnabled = prev })
}

func TestVerifyCacheExact(t *testing.T) {
	setVerifyCache(t, true)
	priv := GenPrivKey()
	pub := priv.PubKey().(PubKey)
	msg := []byte("hello")
	sig, err := priv.Sign(msg)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if !pub.VerifySignature(msg, sig) {
			t.Fatal("valid sig rejected")
		}
	}
	bad := append([]byte(nil), sig...)
	bad[3] ^= 1
	if pub.VerifySignature(msg, bad) {
		t.Fatal("bad sig accepted")
	}
	if pub.VerifySignature([]byte("hellp"), sig) {
		t.Fatal("wrong msg accepted after cache")
	}
	other := GenPrivKey().PubKey().(PubKey)
	if other.VerifySignature(msg, sig) {
		t.Fatal("wrong key accepted after cache")
	}
}

// TestVerifyCacheMatchesStock checks, for random valid and mutated triples, that
// VerifySignature returns the same result with the cache on (cold and warm) as
// with it off.
func TestVerifyCacheMatchesStock(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	type triple struct {
		pub      PubKey
		msg, sig []byte
	}
	var cases []triple
	for i := 0; i < 50; i++ {
		seed := make([]byte, 32)
		r.Read(seed)
		priv := GenPrivKeyFromSecret(seed)
		pub := priv.PubKey().(PubKey)
		msg := make([]byte, r.Intn(200))
		r.Read(msg)
		sig, err := priv.Sign(msg)
		require.NoError(t, err)
		cases = append(cases, triple{pub, msg, sig})
		// Mutations of each component, including swapping in another
		// triple's signature, must never hit the cache.
		badSig := append([]byte(nil), sig...)
		badSig[r.Intn(len(badSig))] ^= 1 << uint(r.Intn(8))
		badMsg := append(append([]byte(nil), msg...), 0)
		badPub := append(PubKey(nil), pub...)
		badPub[r.Intn(len(badPub))] ^= 1
		cases = append(cases, triple{pub, msg, badSig}, triple{pub, badMsg, sig}, triple{badPub, msg, sig},
			triple{pub, msg, sig[:63]})
		if i > 0 {
			prev := cases[(i-1)*5]
			cases = append(cases, triple{pub, msg, prev.sig}, triple{prev.pub, msg, sig})
		}
	}
	want := make([]bool, len(cases))
	setVerifyCache(t, false)
	for i, c := range cases {
		want[i] = c.pub.VerifySignature(c.msg, c.sig)
	}
	setVerifyCache(t, true)
	for round := 0; round < 3; round++ {
		for i, c := range cases {
			require.Equal(t, want[i], c.pub.VerifySignature(c.msg, c.sig), "case %d round %d", i, round)
		}
	}
}

// TestVerifyCacheWrongPubKeyLength checks that a malformed public key behaves as
// stock (ed25519.Verify panics) instead of silently returning false.
func TestVerifyCacheWrongPubKeyLength(t *testing.T) {
	sig := make([]byte, SignatureSize)
	short := PubKey(make([]byte, PubKeySize-1))
	require.Panics(t, func() { stded25519.Verify(stded25519.PublicKey(short), nil, sig) })
	for _, on := range []bool{false, true} {
		setVerifyCache(t, on)
		require.Panics(t, func() { short.VerifySignature(nil, sig) }, "cache=%v", on)
	}
}

func cacheKeyN(i int) [32]byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(i))
	return sha256.Sum256(b[:])
}

// TestVerifyCacheBounded re-hits old entries after each rotation (the pattern
// that used to grow the current generation past its limit) and checks that no
// generation ever exceeds limit.
func TestVerifyCacheBounded(t *testing.T) {
	const limit = 64
	c := newVerifyCache(limit)
	next := 0
	for gen := 0; gen < 20; gen++ {
		for i := 0; i < limit; i++ {
			c.add(cacheKeyN(next))
			next++
		}
		for i := 0; i < next; i++ {
			c.has(cacheKeyN(i))
			require.LessOrEqual(t, len(c.cur), limit)
			require.LessOrEqual(t, len(c.prev), limit)
		}
	}
	// The most recently added key is always retained.
	require.True(t, c.has(cacheKeyN(next-1)))
}

func TestVerifyCacheConcurrent(t *testing.T) {
	setVerifyCache(t, true)
	priv := GenPrivKey()
	pub := priv.PubKey().(PubKey)
	msgs := make([][]byte, 32)
	sigs := make([][]byte, 32)
	for i := range msgs {
		msgs[i] = []byte{byte(i)}
		var err error
		sigs[i], err = priv.Sign(msgs[i])
		require.NoError(t, err)
	}
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := range msgs {
				j := (i + w) % len(msgs)
				if !pub.VerifySignature(msgs[j], sigs[j]) {
					t.Errorf("valid sig %d rejected", j)
				}
				if pub.VerifySignature(msgs[j], sigs[(j+1)%len(sigs)]) {
					t.Errorf("mismatched sig %d accepted", j)
				}
			}
		}(w)
	}
	wg.Wait()
}
