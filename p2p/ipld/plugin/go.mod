module github.com/lazyledger/lazyledger-core/p2p/ipld/plugin

go 1.15

require (
	github.com/ipfs/go-block-format v0.0.2
	github.com/ipfs/go-cid v0.0.7
	github.com/ipfs/go-ipfs v0.7.0
	github.com/ipfs/go-ipfs-api v0.2.0
	github.com/ipfs/go-ipld-format v0.2.0
	github.com/lazyledger/nmt v0.1.0
	// rsmt2d is only used in tests:
	github.com/lazyledger/rsmt2d v0.0.0-20201215203123-e5ec7910ddd4
	github.com/multiformats/go-multihash v0.0.14
)

replace (
	github.com/ipfs/go-ipfs v0.7.0 => github.com/lazyledger/go-ipfs v0.7.1-0.20210205233505-656642674e11
	github.com/ipfs/go-verifcid => github.com/lazyledger/go-verifcid v0.0.2-0.20210205232850-c3e21cfe4064
	github.com/multiformats/go-multihash => github.com/lazyledger/go-multihash v0.0.15-0.20210205224750-88bad1265973
)
