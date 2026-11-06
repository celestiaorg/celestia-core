package p2p

import (
	"errors"
	"net"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/ed25519"
)

func TestNodeInfoValidate(t *testing.T) {

	// empty fails
	ni := DefaultNodeInfo{}
	assert.Error(t, ni.Validate())

	channels := make([]byte, maxNumChannels)
	for i := 0; i < maxNumChannels; i++ {
		channels[i] = byte(i)
	}
	dupChannels := make([]byte, 5) //nolint:prealloc
	copy(dupChannels, channels[:5])
	dupChannels = append(dupChannels, testCh)

	nonASCII := "¢§µ"
	emptyTab := "\t"
	emptySpace := "  "

	testCases := []struct {
		testName         string
		malleateNodeInfo func(*DefaultNodeInfo)
		expectErr        bool
	}{
		{
			"Too Many Channels",
			func(ni *DefaultNodeInfo) { ni.Channels = append(channels, byte(maxNumChannels)) }, //nolint: gocritic
			true,
		},
		{"Duplicate Channel", func(ni *DefaultNodeInfo) { ni.Channels = dupChannels }, true},
		{"Good Channels", func(ni *DefaultNodeInfo) { ni.Channels = ni.Channels[:5] }, false},

		{"Invalid NetAddress", func(ni *DefaultNodeInfo) { ni.ListenAddr = "not-an-address" }, true},
		{"Good NetAddress", func(ni *DefaultNodeInfo) { ni.ListenAddr = "0.0.0.0:26656" }, false},

		{"Non-ASCII Version", func(ni *DefaultNodeInfo) { ni.Version = nonASCII }, true},
		{"Empty tab Version", func(ni *DefaultNodeInfo) { ni.Version = emptyTab }, true},
		{"Empty space Version", func(ni *DefaultNodeInfo) { ni.Version = emptySpace }, true},
		{"Empty Version", func(ni *DefaultNodeInfo) { ni.Version = "" }, false},

		{"Non-ASCII Moniker", func(ni *DefaultNodeInfo) { ni.Moniker = nonASCII }, true},
		{"Empty tab Moniker", func(ni *DefaultNodeInfo) { ni.Moniker = emptyTab }, true},
		{"Empty space Moniker", func(ni *DefaultNodeInfo) { ni.Moniker = emptySpace }, true},
		{"Empty Moniker", func(ni *DefaultNodeInfo) { ni.Moniker = "" }, true},
		{"Good Moniker", func(ni *DefaultNodeInfo) { ni.Moniker = "hey its me" }, false},

		{"Non-ASCII TxIndex", func(ni *DefaultNodeInfo) { ni.Other.TxIndex = nonASCII }, true},
		{"Empty tab TxIndex", func(ni *DefaultNodeInfo) { ni.Other.TxIndex = emptyTab }, true},
		{"Empty space TxIndex", func(ni *DefaultNodeInfo) { ni.Other.TxIndex = emptySpace }, true},
		{"Empty TxIndex", func(ni *DefaultNodeInfo) { ni.Other.TxIndex = "" }, false},
		{"Off TxIndex", func(ni *DefaultNodeInfo) { ni.Other.TxIndex = "off" }, false},

		{"Non-ASCII RPCAddress", func(ni *DefaultNodeInfo) { ni.Other.RPCAddress = nonASCII }, true},
		{"Empty tab RPCAddress", func(ni *DefaultNodeInfo) { ni.Other.RPCAddress = emptyTab }, true},
		{"Empty space RPCAddress", func(ni *DefaultNodeInfo) { ni.Other.RPCAddress = emptySpace }, true},
		{"Empty RPCAddress", func(ni *DefaultNodeInfo) { ni.Other.RPCAddress = "" }, false},
		{"Good RPCAddress", func(ni *DefaultNodeInfo) { ni.Other.RPCAddress = "0.0.0.0:26657" }, false},
	}

	nodeKey := NodeKey{PrivKey: ed25519.GenPrivKey()}
	name := "testing"

	// test case passes
	ni = testNodeInfo(nodeKey.ID(), name).(DefaultNodeInfo)
	ni.Channels = channels
	assert.NoError(t, ni.Validate())

	for _, tc := range testCases {
		ni := testNodeInfo(nodeKey.ID(), name).(DefaultNodeInfo)
		ni.Channels = channels
		tc.malleateNodeInfo(&ni)
		err := ni.Validate()
		if tc.expectErr {
			assert.Error(t, err, tc.testName)
		} else {
			assert.NoError(t, err, tc.testName)
		}
	}

}

func TestNodeInfoCompatible(t *testing.T) {

	nodeKey1 := NodeKey{PrivKey: ed25519.GenPrivKey()}
	nodeKey2 := NodeKey{PrivKey: ed25519.GenPrivKey()}
	name := "testing"

	var newTestChannel byte = 0x2

	// test NodeInfo is compatible
	ni1 := testNodeInfo(nodeKey1.ID(), name).(DefaultNodeInfo)
	ni2 := testNodeInfo(nodeKey2.ID(), name).(DefaultNodeInfo)
	assert.NoError(t, ni1.CompatibleWith(ni2))

	// add another channel; still compatible
	ni2.Channels = append(ni2.Channels, newTestChannel)
	assert.True(t, ni2.HasChannel(newTestChannel))
	assert.NoError(t, ni1.CompatibleWith(ni2))

	// wrong NodeInfo type is not compatible
	_, netAddr := CreateRoutableAddr()
	ni3 := mockNodeInfo{netAddr}
	assert.Error(t, ni1.CompatibleWith(ni3))

	testCases := []struct {
		testName         string
		malleateNodeInfo func(*DefaultNodeInfo)
	}{
		{"Wrong block version", func(ni *DefaultNodeInfo) { ni.ProtocolVersion.Block++ }},
		{"Wrong network", func(ni *DefaultNodeInfo) { ni.Network += "-wrong" }},
		{"No common channels", func(ni *DefaultNodeInfo) { ni.Channels = []byte{newTestChannel} }},
	}

	for _, tc := range testCases {
		ni := testNodeInfo(nodeKey2.ID(), name).(DefaultNodeInfo)
		tc.malleateNodeInfo(&ni)
		assert.Error(t, ni1.CompatibleWith(ni))
	}
}

// TestNodeInfoValidateDoesNotResolveListenAddr checks that validating a
// self-reported ListenAddr never performs a DNS lookup, while malformed
// addresses are still rejected.
func TestNodeInfoValidateDoesNotResolveListenAddr(t *testing.T) {
	lookups := 0
	origLookupIP := lookupIP
	lookupIP = func(host string) ([]net.IP, error) {
		lookups++
		return nil, errors.New("unexpected DNS lookup of " + host)
	}
	t.Cleanup(func() { lookupIP = origLookupIP })

	nodeKey := NodeKey{PrivKey: ed25519.GenPrivKey()}

	testCases := []struct {
		listenAddr string
		expectErr  bool
	}{
		{"peer.example.com:26656", false},
		{"tcp://peer.example.com:26656", false},
		{"1.2.3.4:26656", false},
		{"[::1]:26656", false},
		{"peer.example.com", true},       // no port
		{":26656", true},                 // empty host
		{"peer.example.com:99999", true}, // port out of range
		{"peer.example.com:abc", true},   // non-numeric port
	}

	for _, tc := range testCases {
		ni := testNodeInfo(nodeKey.ID(), "testing").(DefaultNodeInfo)
		ni.ListenAddr = tc.listenAddr
		err := ni.Validate()
		if tc.expectErr {
			assert.Error(t, err, tc.listenAddr)
		} else {
			assert.NoError(t, err, tc.listenAddr)
		}
	}
	require.Zero(t, lookups, "Validate must not resolve the ListenAddr")
}

// TestNodeInfoIPAddressDoesNotResolve checks that NodeInfoIPAddress never
// performs a DNS lookup and only returns IP addresses.
func TestNodeInfoIPAddressDoesNotResolve(t *testing.T) {
	lookups := 0
	origLookupIP := lookupIP
	lookupIP = func(host string) ([]net.IP, error) {
		lookups++
		return nil, errors.New("unexpected DNS lookup of " + host)
	}
	t.Cleanup(func() { lookupIP = origLookupIP })

	nodeKey := NodeKey{PrivKey: ed25519.GenPrivKey()}
	ni := testNodeInfo(nodeKey.ID(), "testing").(DefaultNodeInfo)

	ni.ListenAddr = "1.2.3.4:26656"
	addr, err := NodeInfoIPAddress(ni)
	require.NoError(t, err)
	assert.Equal(t, "1.2.3.4", addr.IP.String())
	assert.EqualValues(t, 26656, addr.Port)
	assert.Equal(t, nodeKey.ID(), addr.ID)

	ni.ListenAddr = "peer.example.com:26656"
	_, err = NodeInfoIPAddress(ni)
	require.Error(t, err)

	require.Zero(t, lookups, "NodeInfoIPAddress must not resolve the ListenAddr")
}
