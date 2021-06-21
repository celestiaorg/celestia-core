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
	"golang.org/x/sync/errgroup"

	"github.com/lazyledger/lazyledger-core/libs/log"
)

func TestDiscovery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	dhts := dhtNet(ctx, t, 2)
	dht1, dht2 := dhts[0], dhts[0]

	id := RandNamespacedCID(t)
	err := dht1.Provide(ctx, id, false)
	require.NoError(t, err)

	prvs, err := dht2.FindProviders(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, dht1.PeerID(), prvs[0].ID, "peer not found")
}

func TestWriteDiscoveryValidateReadData(t *testing.T) {
	const (
		netSize = 4
		edsSize = 4 // TODO(Wondertan): Increase size once race issue is fixed
		samples = 16
	)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	logger := log.TestingLogger()
	dags, dhts := dagNet(ctx, t, netSize)

	gp, execCtx := errgroup.WithContext(ctx)
	eds := make([]*rsmt2d.ExtendedDataSquare, len(dags))
	for i, dag := range dags {
		i, dag := i, dag
		gp.Go(func() (err error) {
			eds[i], err = PutData(execCtx, RandNamespacedShares(t, edsSize*edsSize), dag)
			if err != nil {
				return
			}
			return ProvideData(execCtx, MakeDataHeader(eds[i]), dhts[i], logger)
		})
	}
	err := gp.Wait()
	require.NoError(t, err)

	gp, execCtx = errgroup.WithContext(ctx)
	for i, dag := range dags {
		if i == len(dags)-1 {
			i = 0
		}
		i, dag := i, dag
		gp.Go(func() error {
			exp := eds[i+1]
			return ValidateAvailability(execCtx, dag, MakeDataHeader(exp), samples, func(NamespacedShare) {})
		})
	}
	err = gp.Wait()
	require.NoError(t, err)

	gp, execCtx = errgroup.WithContext(ctx)
	for i, dag := range dags {
		if i == len(dags)-1 {
			i = 0
		}
		i, dag := i, dag
		gp.Go(func() error {
			exp := eds[i+1]
			got, err := RetrieveData(execCtx, MakeDataHeader(exp), dag, rsmt2d.NewRSGF8Codec())
			if err != nil {
				return err
			}
			assert.True(t, EqualEDS(exp, got))
			return nil
		})
	}
	err = gp.Wait()
	require.NoError(t, err)
}

// TODO(Wondertan): Consider making utilities below as public

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
