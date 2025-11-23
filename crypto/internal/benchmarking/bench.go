package benchmarking

import (
	"io"
	"testing"

	"github.com/cometbft/cometbft/crypto"
)

// The code in this file is adapted from agl/ed25519.
// As such it is under the following license.
// Copyright 2012 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found at the bottom of this file.

// Constant message used across signing and verification benchmarks.
var benchmarkMessage = []byte("Hello, world!")

// zeroReader is a dummy implementation of io.Reader that always returns a buffer 
// filled with zero bytes. It is used to provide deterministic and fast input 
// for key generation benchmarks, isolating the key generation logic performance.
type zeroReader struct{}

// Read fills the buffer with zero bytes.
func (zeroReader) Read(buf []byte) (int, error) {
	for i := range buf {
		buf[i] = 0
	}
	return len(buf), nil
}

// BenchmarkKeyGeneration benchmarks the performance of a cryptographic key generation 
// function using a zero-filled reader as the source of "entropy".
func BenchmarkKeyGeneration(b *testing.B, generateKey func(reader io.Reader) crypto.PrivKey) {
	var zero zeroReader
	b.ResetTimer() // Ensure setup time for zeroReader is excluded.
	for i := 0; i < b.N; i++ {
		generateKey(zero)
	}
}

// BenchmarkSigning benchmarks the performance of the signing algorithm 
// using the provided Private Key on a constant message.
func BenchmarkSigning(b *testing.B, priv crypto.PrivKey) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Attempt to sign the benchmark message
		_, err := priv.Sign(benchmarkMessage)

		if err != nil {
			// Report the error and stop the benchmark if signing fails unexpectedly
			b.Fatalf("Signing failed unexpectedly on iteration %d: %v", i, err)
		}
	}
}

// BenchmarkVerification benchmarks the performance of the signature verification 
// algorithm using the provided Private Key on a constant message.
func BenchmarkVerification(b *testing.B, priv crypto.PrivKey) {
	pub := priv.PubKey()
	
	// Pre-sign the message once outside the loop for deterministic verification setup.
	// We use a short message to ensure the time measured is dominated by the crypto 
	// algorithm, not the underlying hashing.
	signature, err := priv.Sign(benchmarkMessage)
	if err != nil {
		b.Fatalf("Failed to sign message for verification setup: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		pub.VerifySignature(benchmarkMessage, signature)
	}
}

// Below is the aforementioned license.

// Copyright (c) 2012 The Go Authors. All rights reserved.

// Redistribution and use in source and binary forms, with or without
// modification, are permitted provided that the following conditions are
// met:

//    * Redistributions of source code must retain the above copyright
// notice, this list of conditions and the following disclaimer.
//    * Redistributions in binary form must reproduce the above
// copyright notice, this list of conditions and the following disclaimer
// in the documentation and/or other materials provided with the
// distribution.
//    * Neither the name of Google Inc. nor the names of its
// contributors may be used to endorse or promote products derived from
// this software without specific prior written permission.

// THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
// "AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
// LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
// A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
// OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
// SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
// LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
// DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
// THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
// (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
// OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
