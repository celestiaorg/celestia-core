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
	github.com/ipfs/go-ipfs v0.7.0 => github.com/lazyledger/go-ipfs v0.7.1-0.20210205200917-d54b4a6b16b2
	github.com/ipfs/go-verifcid => github.com/lazyledger/go-verifcid v0.0.2-0.20210202003519-bbb215fd683e
	github.com/multiformats/go-multihash => github.com/lazyledger/go-multihash v0.0.15-0.20210201232637-a31dec8c92fa
)
