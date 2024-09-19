package p2p

import (
	"fmt"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/multiformats/go-multiaddr"
)

type PC struct {
	conn            network.Conn
	openedNewStream chan int
}

var _ network.Notifiee = &Notifee{}

type Notifee struct {
	peerCons chan PC
	streams  map[string]network.Stream
}

func (n *Notifee) Listen(net network.Network, addr multiaddr.Multiaddr) {
	fmt.Println("Listening on:", addr)
}

func (n *Notifee) ListenClose(net network.Network, addr multiaddr.Multiaddr) {
	fmt.Println("Stopped listening on:", addr)
}

func (n *Notifee) Connected(net network.Network, conn network.Conn) {
	fmt.Println("New peer connected:", conn.RemotePeer().String())
	n.peerCons <- PC{conn, make(chan int)}
}

func (n *Notifee) Disconnected(net network.Network, conn network.Conn) {
	fmt.Println("Peer disconnected:", conn.RemotePeer().String())
}

func (n *Notifee) OpenedStream(net network.Network, stream network.Stream) {
	fmt.Println("New stream opened from:", stream.Conn().RemotePeer().String())
	stream.ID()
}

func (n *Notifee) ClosedStream(net network.Network, stream network.Stream) {
	fmt.Println("Stream closed from:", stream.Conn().RemotePeer().String())
}
