module github.com/lazyledger/lazyledger-core/p2p/ipld/plugin

go 1.15

require (
	github.com/ipfs/go-block-format v0.0.2
	github.com/ipfs/go-cid v0.0.7
	github.com/ipfs/go-ipfs v0.8.0
	github.com/ipfs/go-ipfs-api v0.2.0
	github.com/ipfs/go-ipld-format v0.2.0
	github.com/ipfs/go-verifcid v0.0.1
	github.com/lazyledger/nmt v0.3.1
	// rsmt2d is only used in tests:
	github.com/lazyledger/rsmt2d v0.2.0
	github.com/multiformats/go-multihash v0.0.14
)

replace (
	// If you encounter:
	//  plugin was built with a different version of package X
	// overwrite below line with a local directory with the corresponding version of go-ipfs checked out
	github.com/ipfs/go-ipfs => github.com/lazyledger/go-ipfs v0.8.0-lazypatch
	github.com/ipfs/go-verifcid => github.com/lazyledger/go-verifcid v0.0.1-lazypatch
)
