package ipld

import (
	"context"
	"testing"
	"time"

	"github.com/ipfs/go-bitswap"
	"github.com/ipfs/go-bitswap/network"
	"github.com/ipfs/go-blockservice"
	ds "github.com/ipfs/go-datastore"
	dssync "github.com/ipfs/go-datastore/sync"
	blockstore "github.com/ipfs/go-ipfs-blockstore"
	ipld "github.com/ipfs/go-ipld-format"
	"github.com/ipfs/go-merkledag"
	"github.com/lazyledger/rsmt2d"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	mocknet "github.com/libp2p/go-libp2p/p2p/net/mock"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/lazyledger/lazyledger-core/ipfs/plugin"
	"github.com/lazyledger/lazyledger-core/libs/log"
	"github.com/lazyledger/lazyledger-core/types"
	"github.com/lazyledger/lazyledger-core/types/consts"
)

func TestDiscovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	dhts := dhtNet(ctx, t, 2)
	dht1, dht2 := dhts[0], dhts[0]

	data := generateRandomBlockData(64, consts.MsgShareSize-2)
	b := &types.Block{
		Data:       data,
		LastCommit: &types.Commit{},
	}
	b.Hash()

	id, err := plugin.CidFromNamespacedSha256(b.DataAvailabilityHeader.RowsRoots[0].Bytes())
	require.NoError(t, err)

	err = dht1.Provide(ctx, id, false)
	require.NoError(t, err)

	prvs, err := dht2.FindProviders(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, dht1.PeerID(), prvs[0].ID, "peer not found")
}

func TestWriteDiscoveryReadData(t *testing.T) {
	logger := log.TestingLogger()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	dags, dhts := dagNet(ctx, t, 5)
	blocks := make([]*types.Block, len(dags))
	for i, dag := range dags {
		data := generateRandomBlockData(64, consts.MsgShareSize-2)
		b := &types.Block{
			Data:       data,
			LastCommit: &types.Commit{},
		}
		b.Hash()
		blocks[i] = b

		err := PutBlock(ctx, dag, blocks[i], dhts[i], logger)
		require.NoError(t, err)
	}

	for i, dag := range dags {
		if i == len(dags)-1 {
			i = 0
		}

		exp := blocks[i+1]
		actual, err := RetrieveBlockData(ctx, &exp.DataAvailabilityHeader, dag, rsmt2d.NewRSGF8Codec())
		assert.NoError(t, err)
		assert.EqualValues(t, exp.Data.Txs, actual.Txs, "blocks are not equal")
	}
}

func dagNet(ctx context.Context, t *testing.T, num int) ([]ipld.DAGService, []*dht.IpfsDHT) {
	net := mocknet.New(ctx)
	_, medium := dagNode(ctx, t, net)
	dags, dhts := make([]ipld.DAGService, num), make([]*dht.IpfsDHT, num)
	for i := range dags {
		dags[i], dhts[i] = dagNode(ctx, t, net)
	}
	bootstrap(ctx, t, net, medium, dhts...)
	return dags, dhts
}

func dhtNet(ctx context.Context, t *testing.T, num int) []*dht.IpfsDHT {
	net := mocknet.New(ctx)
	medium := dhtNode(ctx, t, net)
	dhts := make([]*dht.IpfsDHT, num)
	for i := range dhts {
		dhts[i] = dhtNode(ctx, t, net)
	}
	bootstrap(ctx, t, net, medium, dhts...)
	return dhts
}

func dagNode(ctx context.Context, t *testing.T, net mocknet.Mocknet) (ipld.DAGService, *dht.IpfsDHT) {
	dstore := dssync.MutexWrap(ds.NewMapDatastore())
	bstore := blockstore.NewBlockstore(dstore)
	routing := dhtNode(ctx, t, net)
	bs := bitswap.New(ctx, network.NewFromIpfsHost(routing.Host(), routing), bstore, bitswap.ProvideEnabled(false))
	return merkledag.NewDAGService(blockservice.New(bstore, bs)), routing
}

func dhtNode(ctx context.Context, t *testing.T, net mocknet.Mocknet) *dht.IpfsDHT {
	host, err := net.GenPeer()
	require.NoError(t, err)
	dstore := dssync.MutexWrap(ds.NewMapDatastore())
	routing, err := dht.New(ctx, host, dht.Datastore(dstore), dht.Mode(dht.ModeServer), dht.BootstrapPeers())
	require.NoError(t, err)
	return routing
}

func bootstrap(ctx context.Context, t *testing.T, net mocknet.Mocknet, bstrapper *dht.IpfsDHT, peers ...*dht.IpfsDHT) {
	err := net.LinkAll()
	require.NoError(t, err)
	for _, p := range peers {
		_, err := net.ConnectPeers(bstrapper.PeerID(), p.PeerID())
		require.NoError(t, err)
		err = bstrapper.Bootstrap(ctx)
		require.NoError(t, err)
	}
	err = bstrapper.Bootstrap(ctx)
	require.NoError(t, err)
}
