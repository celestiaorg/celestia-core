module github.com/lazyledger/lazyledger-core/p2p/ipld/plugin

go 1.15

require (
	github.com/ipfs/go-block-format v0.0.2
	github.com/ipfs/go-cid v0.0.7
	github.com/ipfs/go-ipfs v0.7.0
	github.com/ipfs/go-ipfs-api v0.2.0
	github.com/ipfs/go-ipld-format v0.2.0
	// TODO(ismail): change this to a tagged version after
	// https://github.com/lazyledger/nmt/pull/19 gets merged
	github.com/lazyledger/nmt v0.1.1-0.20210212160145-d6f4312f0a8d
	// rsmt2d is only used in tests:
	github.com/lazyledger/rsmt2d v0.0.0-20201215203123-e5ec7910ddd4
	github.com/multiformats/go-multihash v0.0.14
)

replace (
	github.com/ipfs/go-ipfs v0.7.0 => github.com/lazyledger/go-ipfs v0.7.1-0.20210213193504-c65604ccb2ed
	github.com/ipfs/go-verifcid => github.com/lazyledger/go-verifcid v0.0.2-0.20210213182512-19890b0d114d
)
