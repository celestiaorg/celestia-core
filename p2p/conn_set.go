package p2p

import (
	"fmt"
	"github.com/quic-go/quic-go"
	"net"

	cmtsync "github.com/tendermint/tendermint/libs/sync"
)

// ConnSet is a lookup table for connections and all their ips.
type ConnSet interface {
	Has(quic.Connection) bool
	HasIP(net.IP) bool
	Set(quic.Connection, []net.IP)
	Remove(quic.Connection)
	RemoveAddr(net.Addr)
}

type connSetItem struct {
	conn quic.Connection
	ips  []net.IP
}

type connSet struct {
	cmtsync.RWMutex

	conns map[string]connSetItem
}

// NewConnSet returns a ConnSet implementation.
func NewConnSet() ConnSet {
	return &connSet{
		conns: map[string]connSetItem{},
	}
}

func (cs *connSet) Has(c quic.Connection) bool {
	cs.RLock()
	defer cs.RUnlock()

	fmt.Println("has")
	for key, val := range cs.conns {
		fmt.Printf("%s:%v\n", key, val.ips)
	}
	fmt.Printf("looking for %s\n", c.RemoteAddr().String())
	_, ok := cs.conns[c.RemoteAddr().String()]

	return ok
}

func (cs *connSet) HasIP(ip net.IP) bool {
	cs.RLock()
	defer cs.RUnlock()

	for _, c := range cs.conns {
		for _, known := range c.ips {
			if known.Equal(ip) {
				return true
			}
		}
	}

	return false
}

func (cs *connSet) Remove(c quic.Connection) {
	cs.Lock()
	defer cs.Unlock()

	delete(cs.conns, c.RemoteAddr().String())
}

func (cs *connSet) RemoveAddr(addr net.Addr) {
	cs.Lock()
	defer cs.Unlock()

	delete(cs.conns, addr.String())
}

func (cs *connSet) Set(c quic.Connection, ips []net.IP) {
	cs.Lock()
	defer cs.Unlock()

	cs.conns[c.RemoteAddr().String()] = connSetItem{
		conn: c,
		ips:  ips,
	}
}
