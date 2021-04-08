module github.com/lazyledger/lazyledger-core

go 1.15

require (
	github.com/BurntSushi/toml v0.3.1
	github.com/ChainSafe/go-schnorrkel v0.0.0-20200405005733-88cbf1b4c40d
	github.com/Workiva/go-datastructures v1.0.52
	github.com/btcsuite/btcd v0.21.0-beta
	github.com/btcsuite/btcutil v1.0.2
	github.com/fortytw2/leaktest v1.3.0
	github.com/go-kit/kit v0.10.0
	github.com/go-logfmt/logfmt v0.5.0
	github.com/gogo/protobuf v1.3.2
	github.com/golang/protobuf v1.4.3
	github.com/gorilla/websocket v1.4.2
	github.com/gtank/merlin v0.1.1
	github.com/hdevalence/ed25519consensus v0.0.0-20201207055737-7fde80a9d5ff
	github.com/ipfs/go-cid v0.0.7
	github.com/ipfs/go-ipfs v0.8.0
	github.com/ipfs/go-ipfs-config v0.12.0
	github.com/ipfs/go-ipld-format v0.2.0
	github.com/ipfs/go-path v0.0.9 // indirect
	github.com/ipfs/interface-go-ipfs-core v0.4.0
	github.com/lazyledger/lazyledger-core/p2p/ipld/plugin v0.0.0-20210219190522-0eccfb24e2aa
	github.com/lazyledger/nmt v0.3.1
	github.com/lazyledger/rsmt2d v0.1.1-0.20210406153014-e1fd589bdb09
	github.com/libp2p/go-buffer-pool v0.0.2
	github.com/minio/highwayhash v1.0.1
	github.com/multiformats/go-multihash v0.0.14
	github.com/niemeyer/pretty v0.0.0-20200227124842-a10e7caefd8e // indirect
	github.com/petermattis/goid v0.0.0-20180202154549-b0b1615b78e5 // indirect
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.8.0
	github.com/rcrowley/go-metrics v0.0.0-20200313005456-10cdbea86bc0
	github.com/rs/cors v1.7.0
	github.com/sasha-s/go-deadlock v0.2.0
	github.com/snikch/goodman v0.0.0-20171125024755-10e37e294daa
	github.com/spf13/cobra v1.1.1
	github.com/spf13/viper v1.7.1
	github.com/stretchr/testify v1.7.0
	github.com/tendermint/tm-db v0.6.4
	golang.org/x/crypto v0.0.0-20210322153248-0c34fe9e7dc2
	golang.org/x/exp v0.0.0-20200331195152-e8c3332aa8e5 // indirect
	golang.org/x/net v0.0.0-20210226172049-e18ecbb05110
	google.golang.org/genproto v0.0.0-20201119123407-9b1e624d6bc4 // indirect
	google.golang.org/grpc v1.35.0
)

replace (
	github.com/ipfs/go-ipfs => github.com/lazyledger/go-ipfs v0.8.0-lazypatch
	// adding an extra replace statement here enforces usage of our fork of go-cerifcid
	github.com/ipfs/go-verifcid => github.com/lazyledger/go-verifcid v0.0.1-lazypatch
	github.com/lazyledger/lazyledger-core/p2p/ipld/plugin => ./p2p/ipld/plugin
)
