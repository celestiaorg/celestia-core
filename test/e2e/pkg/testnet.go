package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/cometbft/cometbft/crypto"
	"github.com/cometbft/cometbft/crypto/ed25519"
	"github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cometbft/cometbft/crypto/sr25519"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	"github.com/cometbft/cometbft/types"

	_ "embed"
)

const (
	randomSeed               int64  = 2308084734268
	proxyPortFirst           uint32 = 5701
	prometheusProxyPortFirst uint32 = 6701

	defaultBatchSize   = 2
	defaultConnections = 1
	defaultTxSizeBytes = 1024

	localVersion = "cometbft/e2e-node:local-version"
)

type (
	Mode         string
	Protocol     string
	Perturbation string
)

const (
	ModeValidator Mode = "validator"
	ModeFull      Mode = "full"
	ModeLight     Mode = "light"
	ModeSeed      Mode = "seed"

	ProtocolBuiltin         Protocol = "builtin"
	ProtocolBuiltinConnSync Protocol = "builtin_connsync"
	ProtocolFile            Protocol = "file"
	ProtocolGRPC            Protocol = "grpc"
	ProtocolTCP             Protocol = "tcp"
	ProtocolUNIX            Protocol = "unix"

	PerturbationDisconnect Perturbation = "disconnect"
	PerturbationKill       Perturbation = "kill"
	PerturbationPause      Perturbation = "pause"
	PerturbationRestart    Perturbation = "restart"
	PerturbationUpgrade    Perturbation = "upgrade"

	EvidenceAgeHeight int64         = 14
	EvidenceAgeTime   time.Duration = 1500 * time.Millisecond
)

// Testnet represents a single testnet.
type Testnet struct {
	Name                                                 string
	File                                                 string
	Dir                                                  string
	IP                                                   *net.IPNet
	InitialHeight                                        int64
	InitialState                                         map[string]string
	Validators                                           map[*Node]int64
	ValidatorUpdates                                     map[int64]map[*Node]int64
	Nodes                                                []*Node
	KeyType                                              string
	Evidence                                             int
	LoadTxSizeBytes                                      int
	LoadTxBatchSize                                      int
	LoadTxConnections                                    int
	LoadMaxTxs                                           int
	ABCIProtocol                                         string
	PrepareProposalDelay                                 time.Duration
	ProcessProposalDelay                                 time.Duration
	CheckTxDelay                                         time.Duration
	VoteExtensionDelay                                   time.Duration
	FinalizeBlockDelay                                   time.Duration
	UpgradeVersion                                       string
	LogLevel                                             string
	LogFormat                                            string
	Prometheus                                           bool
	BlockMaxBytes                                        int64
	VoteExtensionsEnableHeight                           int64
	VoteExtensionsUpdateHeight                           int64
	ExperimentalMaxGossipConnectionsToPersistentPeers    uint
	ExperimentalMaxGossipConnectionsToNonPersistentPeers uint

	MaxInboundConnections  int
	MaxOutboundConnections int
}

// Node represents a CometBFT node in a testnet.
type Node struct {
	Name                string
	Version             string
	Testnet             *Testnet
	Mode                Mode
	PrivvalKey          crypto.PrivKey
	NodeKey             crypto.PrivKey
	InternalIP          net.IP
	ExternalIP          net.IP
	ProxyPort           uint32
	StartAt             int64
	BlockSyncVersion    string
	StateSync           bool
	Database            string
	ABCIProtocol        Protocol
	PrivvalProtocol     Protocol
	PersistInterval     uint64
	SnapshotInterval    uint64
	RetainBlocks        uint64
	Seeds               []*Node
	PersistentPeers     []*Node
	Perturbations       []Perturbation
	SendNoLoad          bool
	Prometheus          bool
	PrometheusProxyPort uint32

	MaxInboundConnections  int
	MaxOutboundConnections int

	TracePushConfig       string
	TracePullAddress      string
	PyroscopeURL          string
	PyroscopeTrace        bool
	PyroscopeProfileTypes []string

	DisablePropagationReactor bool
}

// LoadTestnet loads a testnet from a manifest file, using the filename to
// determine the testnet name and directory (from the basename of the file).
// The testnet generation must be deterministic, since it is generated
// separately by the runner and the test cases. For this reason, testnets use a
// random seed to generate e.g. keys.
func LoadTestnet(file string, ifd InfrastructureData) (*Testnet, error) {
	manifest, err := LoadManifest(file)
	if err != nil {
		return nil, err
	}
	return NewTestnetFromManifest(manifest, file, ifd)
}

// NewTestnetFromManifest creates and validates a testnet from a manifest
func NewTestnetFromManifest(manifest Manifest, file string, ifd InfrastructureData) (*Testnet, error) {
	dir := strings.TrimSuffix(file, filepath.Ext(file))

	keyGen := newKeyGenerator(randomSeed)
	prometheusProxyPortGen := newPortGenerator(prometheusProxyPortFirst)
	_, ipNet, err := net.ParseCIDR(ifd.Network)
	if err != nil {
		return nil, fmt.Errorf("invalid IP network address %q: %w", ifd.Network, err)
	}

	testnet := &Testnet{
		Name:                       filepath.Base(dir),
		File:                       file,
		Dir:                        dir,
		IP:                         ipNet,
		InitialHeight:              1,
		InitialState:               manifest.InitialState,
		Validators:                 map[*Node]int64{},
		ValidatorUpdates:           map[int64]map[*Node]int64{},
		Nodes:                      []*Node{},
		Evidence:                   manifest.Evidence,
		LoadTxSizeBytes:            manifest.LoadTxSizeBytes,
		LoadTxBatchSize:            manifest.LoadTxBatchSize,
		LoadTxConnections:          manifest.LoadTxConnections,
		LoadMaxTxs:                 manifest.LoadMaxTxs,
		ABCIProtocol:               manifest.ABCIProtocol,
		PrepareProposalDelay:       manifest.PrepareProposalDelay,
		ProcessProposalDelay:       manifest.ProcessProposalDelay,
		CheckTxDelay:               manifest.CheckTxDelay,
		VoteExtensionDelay:         manifest.VoteExtensionDelay,
		FinalizeBlockDelay:         manifest.FinalizeBlockDelay,
		UpgradeVersion:             manifest.UpgradeVersion,
		LogLevel:                   manifest.LogLevel,
		LogFormat:                  manifest.LogFormat,
		Prometheus:                 manifest.Prometheus,
		BlockMaxBytes:              manifest.BlockMaxBytes,
		VoteExtensionsEnableHeight: manifest.VoteExtensionsEnableHeight,
		VoteExtensionsUpdateHeight: manifest.VoteExtensionsUpdateHeight,
		ExperimentalMaxGossipConnectionsToPersistentPeers:    manifest.ExperimentalMaxGossipConnectionsToPersistentPeers,
		ExperimentalMaxGossipConnectionsToNonPersistentPeers: manifest.ExperimentalMaxGossipConnectionsToNonPersistentPeers,

		MaxInboundConnections:  manifest.MaxInboundConnections,
		MaxOutboundConnections: manifest.MaxOutboundConnections,
	}
	if len(manifest.KeyType) != 0 {
		testnet.KeyType = manifest.KeyType
	}
	if manifest.InitialHeight > 0 {
		testnet.InitialHeight = manifest.InitialHeight
	}
	if testnet.ABCIProtocol == "" {
		testnet.ABCIProtocol = string(ProtocolBuiltin)
	}
	if testnet.UpgradeVersion == "" {
		testnet.UpgradeVersion = localVersion
	}
	if testnet.LoadTxConnections == 0 {
		testnet.LoadTxConnections = defaultConnections
	}
	if testnet.LoadTxBatchSize == 0 {
		testnet.LoadTxBatchSize = defaultBatchSize
	}
	if testnet.LoadTxSizeBytes == 0 {
		testnet.LoadTxSizeBytes = defaultTxSizeBytes
	}

	for _, name := range sortNodeNames(manifest) {
		nodeManifest := manifest.Nodes[name]
		ind, ok := ifd.Instances[name]
		if !ok {
			return nil, fmt.Errorf("information for node '%s' missing from infrastructure data", name)
		}
		extIP := ind.ExtIPAddress
		if len(extIP) == 0 {
			extIP = ind.IPAddress
		}
		v := nodeManifest.Version
		if v == "" {
			v = localVersion
		}

		node := &Node{
			Name:             name,
			Version:          v,
			Testnet:          testnet,
			PrivvalKey:       keyGen.Generate(manifest.KeyType),
			NodeKey:          keyGen.Generate("ed25519"),
			InternalIP:       ind.IPAddress,
			ExternalIP:       extIP,
			ProxyPort:        ind.Port,
			Mode:             ModeValidator,
			Database:         "goleveldb",
			ABCIProtocol:     Protocol(testnet.ABCIProtocol),
			PrivvalProtocol:  ProtocolFile,
			StartAt:          nodeManifest.StartAt,
			BlockSyncVersion: nodeManifest.BlockSyncVersion,
			StateSync:        nodeManifest.StateSync,
			PersistInterval:  1,
			SnapshotInterval: nodeManifest.SnapshotInterval,
			RetainBlocks:     nodeManifest.RetainBlocks,
			Perturbations:    []Perturbation{},
			SendNoLoad:       nodeManifest.SendNoLoad,
			Prometheus:       testnet.Prometheus,

			TracePushConfig:       ifd.TracePushConfig,
			TracePullAddress:      ifd.TracePullAddress,
			PyroscopeURL:          ifd.PyroscopeURL,
			PyroscopeTrace:        ifd.PyroscopeTrace,
			PyroscopeProfileTypes: ifd.PyroscopeProfileTypes,

			DisablePropagationReactor: nodeManifest.DisablePropagationReactor,
		}
		if node.StartAt == testnet.InitialHeight {
			node.StartAt = 0 // normalize to 0 for initial nodes, since code expects this
		}
		if node.BlockSyncVersion == "" {
			node.BlockSyncVersion = "v0"
		}
		if nodeManifest.Mode != "" {
			node.Mode = Mode(nodeManifest.Mode)
		}
		if node.Mode == ModeLight {
			node.ABCIProtocol = ProtocolBuiltin
		}
		if nodeManifest.Database != "" {
			node.Database = nodeManifest.Database
		}
		if nodeManifest.PrivvalProtocol != "" {
			node.PrivvalProtocol = Protocol(nodeManifest.PrivvalProtocol)
		}
		if nodeManifest.PersistInterval != nil {
			node.PersistInterval = *nodeManifest.PersistInterval
		}
		if node.Prometheus {
			node.PrometheusProxyPort = prometheusProxyPortGen.Next()
		}
		for _, p := range nodeManifest.Perturb {
			node.Perturbations = append(node.Perturbations, Perturbation(p))
		}
		if node.MaxInboundConnections < 0 {
			return nil, errors.New("MaxInboundConnections must not be negative")
		}
		if node.MaxOutboundConnections < 0 {
			return nil, errors.New("MaxOutboundConnections must not be negative")
		}
		testnet.Nodes = append(testnet.Nodes, node)
	}

	// We do a second pass to set up seeds and persistent peers, which allows graph cycles.
	for _, node := range testnet.Nodes {
		nodeManifest, ok := manifest.Nodes[node.Name]
		if !ok {
			return nil, fmt.Errorf("failed to look up manifest for node %q", node.Name)
		}
		for _, seedName := range nodeManifest.Seeds {
			seed := testnet.LookupNode(seedName)
			if seed == nil {
				return nil, fmt.Errorf("unknown seed %q for node %q", seedName, node.Name)
			}
			node.Seeds = append(node.Seeds, seed)
		}
		for _, peerName := range nodeManifest.PersistentPeers {
			peer := testnet.LookupNode(peerName)
			if peer == nil {
				return nil, fmt.Errorf("unknown persistent peer %q for node %q", peerName, node.Name)
			}
			node.PersistentPeers = append(node.PersistentPeers, peer)
		}

		// If there are no seeds or persistent peers specified, default to persistent
		// connections to all other nodes.
		if len(node.PersistentPeers) == 0 && len(node.Seeds) == 0 {
			for _, peer := range testnet.Nodes {
				if peer.Name == node.Name {
					continue
				}
				node.PersistentPeers = append(node.PersistentPeers, peer)
			}
		}
	}

	// Set up genesis validators. If not specified explicitly, use all validator nodes.
	if manifest.Validators != nil {
		for validatorName, power := range *manifest.Validators {
			validator := testnet.LookupNode(validatorName)
			if validator == nil {
				return nil, fmt.Errorf("unknown validator %q", validatorName)
			}
			testnet.Validators[validator] = power
		}
	} else {
		for _, node := range testnet.Nodes {
			if node.Mode == ModeValidator {
				testnet.Validators[node] = 100
			}
		}
	}

	// Set up validator updates.
	for heightStr, validators := range manifest.ValidatorUpdates {
		height, err := strconv.Atoi(heightStr)
		if err != nil {
			return nil, fmt.Errorf("invalid validator update height %q: %w", height, err)
		}
		valUpdate := map[*Node]int64{}
		for name, power := range validators {
			node := testnet.LookupNode(name)
			if node == nil {
				return nil, fmt.Errorf("unknown validator %q for update at height %v", name, height)
			}
			valUpdate[node] = power
		}
		testnet.ValidatorUpdates[int64(height)] = valUpdate
	}

	return testnet, testnet.Validate()
}

// Validate validates a testnet.
func (t Testnet) Validate() error {
	if t.Name == "" {
		return errors.New("network has no name")
	}
	if t.IP == nil {
		return errors.New("network has no IP")
	}
	if len(t.Nodes) == 0 {
		return errors.New("network has no nodes")
	}
	if t.BlockMaxBytes > types.MaxBlockSizeBytes {
		return fmt.Errorf("value of BlockMaxBytes cannot be higher than %d", types.MaxBlockSizeBytes)
	}
	if t.VoteExtensionsUpdateHeight < -1 {
		return fmt.Errorf("value of VoteExtensionsUpdateHeight must be positive, 0 (InitChain), "+
			"or -1 (Genesis); update height %d", t.VoteExtensionsUpdateHeight)
	}
	if t.VoteExtensionsEnableHeight < 0 {
		return fmt.Errorf("value of VoteExtensionsEnableHeight must be positive, or 0 (disable); "+
			"enable height %d", t.VoteExtensionsEnableHeight)
	}
	if t.VoteExtensionsUpdateHeight > 0 && t.VoteExtensionsUpdateHeight < t.InitialHeight {
		return fmt.Errorf("a value of VoteExtensionsUpdateHeight greater than 0 "+
			"must not be less than InitialHeight; "+
			"update height %d, initial height %d",
			t.VoteExtensionsUpdateHeight, t.InitialHeight,
		)
	}
	if t.VoteExtensionsEnableHeight > 0 {
		if t.VoteExtensionsEnableHeight < t.InitialHeight {
			return fmt.Errorf("a value of VoteExtensionsEnableHeight greater than 0 "+
				"must not be less than InitialHeight; "+
				"enable height %d, initial height %d",
				t.VoteExtensionsEnableHeight, t.InitialHeight,
			)
		}
		if t.VoteExtensionsEnableHeight <= t.VoteExtensionsUpdateHeight {
			return fmt.Errorf("a value of VoteExtensionsEnableHeight greater than 0 "+
				"must be greater than VoteExtensionsUpdateHeight; "+
				"update height %d, enable height %d",
				t.VoteExtensionsUpdateHeight, t.VoteExtensionsEnableHeight,
			)
		}
	}
	for _, node := range t.Nodes {
		if err := node.Validate(t); err != nil {
			return fmt.Errorf("invalid node %q: %w", node.Name, err)
		}
	}
	return nil
}

// Validate validates a node.
func (n Node) Validate(testnet Testnet) error {
	if n.Name == "" {
		return errors.New("node has no name")
	}
	if n.InternalIP == nil {
		return errors.New("node has no IP address")
	}
	if !testnet.IP.Contains(n.InternalIP) {
		return fmt.Errorf("node IP %v is not in testnet network %v", n.InternalIP, testnet.IP)
	}
	if n.ProxyPort == n.PrometheusProxyPort {
		return fmt.Errorf("node local port %v used also for Prometheus local port", n.ProxyPort)
	}
	if n.ProxyPort > 0 && n.ProxyPort <= 1024 {
		return fmt.Errorf("local port %v must be >1024", n.ProxyPort)
	}
	if n.PrometheusProxyPort > 0 && n.PrometheusProxyPort <= 1024 {
		return fmt.Errorf("local port %v must be >1024", n.PrometheusProxyPort)
	}
	for _, peer := range testnet.Nodes {
		if peer.Name != n.Name && peer.ProxyPort == n.ProxyPort && peer.ExternalIP.Equal(n.ExternalIP) {
			return fmt.Errorf("peer %q also has local port %v", peer.Name, n.ProxyPort)
		}
		if n.PrometheusProxyPort > 0 {
			if peer.Name != n.Name && peer.PrometheusProxyPort == n.PrometheusProxyPort {
				return fmt.Errorf("peer %q also has local port %v", peer.Name, n.PrometheusProxyPort)
			}
		}
	}
	switch n.BlockSyncVersion {
	case "v0":
	default:
		return fmt.Errorf("invalid block sync setting %q", n.BlockSyncVersion)
	}
	switch n.Database {
	case "goleveldb", "pebbledb", "badgerdb":
	default:
		return fmt.Errorf("invalid database setting %q", n.Database)
	}
	switch n.ABCIProtocol {
	case ProtocolBuiltin, ProtocolBuiltinConnSync, ProtocolUNIX, ProtocolTCP, ProtocolGRPC:
	default:
		return fmt.Errorf("invalid ABCI protocol setting %q", n.ABCIProtocol)
	}
	if n.Mode == ModeLight && n.ABCIProtocol != ProtocolBuiltin && n.ABCIProtocol != ProtocolBuiltinConnSync {
		return errors.New("light client must use builtin protocol")
	}
	switch n.PrivvalProtocol {
	case ProtocolFile, ProtocolUNIX, ProtocolTCP:
	default:
		return fmt.Errorf("invalid privval protocol setting %q", n.PrivvalProtocol)
	}

	if n.StartAt > 0 && n.StartAt < n.Testnet.InitialHeight {
		return fmt.Errorf("cannot start at height %v lower than initial height %v",
			n.StartAt, n.Testnet.InitialHeight)
	}
	if n.StateSync && n.StartAt == 0 {
		return errors.New("state synced nodes cannot start at the initial height")
	}
	if n.RetainBlocks != 0 && n.RetainBlocks < uint64(EvidenceAgeHeight) {
		return fmt.Errorf("retain_blocks must be 0 or be greater or equal to max evidence age (%d)",
			EvidenceAgeHeight)
	}
	if n.PersistInterval == 0 && n.RetainBlocks > 0 {
		return errors.New("persist_interval=0 requires retain_blocks=0")
	}
	if n.PersistInterval > 1 && n.RetainBlocks > 0 && n.RetainBlocks < n.PersistInterval {
		return errors.New("persist_interval must be less than or equal to retain_blocks")
	}
	if n.SnapshotInterval > 0 && n.RetainBlocks > 0 && n.RetainBlocks < n.SnapshotInterval {
		return errors.New("snapshot_interval must be less than er equal to retain_blocks")
	}

	var upgradeFound bool
	for _, perturbation := range n.Perturbations {
		switch perturbation {
		case PerturbationUpgrade:
			if upgradeFound {
				return fmt.Errorf("'upgrade' perturbation can appear at most once per node")
			}
			upgradeFound = true
		case PerturbationDisconnect, PerturbationKill, PerturbationPause, PerturbationRestart:
		default:
			return fmt.Errorf("invalid perturbation %q", perturbation)
		}
	}

	return nil
}

// LookupNode looks up a node by name. For now, simply do a linear search.
func (t Testnet) LookupNode(name string) *Node {
	for _, node := range t.Nodes {
		if node.Name == name {
			return node
		}
	}
	return nil
}

// ArchiveNodes returns a list of archive nodes that start at the initial height
// and contain the entire blockchain history. They are used e.g. as light client
// RPC servers.
func (t Testnet) ArchiveNodes() []*Node {
	nodes := []*Node{}
	for _, node := range t.Nodes {
		if !node.Stateless() && node.StartAt == 0 && node.RetainBlocks == 0 {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

// RandomNode returns a random non-seed node.
func (t Testnet) RandomNode() *Node {
	for {
		node := t.Nodes[rand.Intn(len(t.Nodes))] //nolint:gosec
		if node.Mode != ModeSeed {
			return node
		}
	}
}

// IPv6 returns true if the testnet is an IPv6 network.
func (t Testnet) IPv6() bool {
	return t.IP.IP.To4() == nil
}

// HasPerturbations returns whether the network has any perturbations.
func (t Testnet) HasPerturbations() bool {
	for _, node := range t.Nodes {
		if len(node.Perturbations) > 0 {
			return true
		}
	}
	return false
}

//go:embed templates/prometheus-yaml.tmpl
var prometheusYamlTemplate string

func (t Testnet) prometheusConfigBytes() ([]byte, error) {
	tmpl, err := template.New("prometheus-yaml").Parse(prometheusYamlTemplate)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, t)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (t Testnet) WritePrometheusConfig() error {
	bytes, err := t.prometheusConfigBytes()
	if err != nil {
		return err
	}
	err = os.WriteFile(filepath.Join(t.Dir, "prometheus.yaml"), bytes, 0o644) //nolint:gosec
	if err != nil {
		return err
	}
	return nil
}

// Address returns a P2P endpoint address for the node.
func (n Node) AddressP2P(withID bool) string {
	ip := n.InternalIP.String()
	if n.InternalIP.To4() == nil {
		// IPv6 addresses must be wrapped in [] to avoid conflict with : port separator
		ip = fmt.Sprintf("[%v]", ip)
	}
	addr := fmt.Sprintf("%v:26656", ip)
	if withID {
		addr = fmt.Sprintf("%x@%v", n.NodeKey.PubKey().Address().Bytes(), addr)
	}
	return addr
}

// Address returns an RPC endpoint address for the node.
func (n Node) AddressRPC() string {
	ip := n.InternalIP.String()
	if n.InternalIP.To4() == nil {
		// IPv6 addresses must be wrapped in [] to avoid conflict with : port separator
		ip = fmt.Sprintf("[%v]", ip)
	}
	return fmt.Sprintf("%v:26657", ip)
}

// Client returns an RPC client for a node.
func (n Node) Client() (*rpchttp.HTTP, error) {
	return rpchttp.New(fmt.Sprintf("http://%s:%v", n.ExternalIP, n.ProxyPort), "/websocket")
}

// Stateless returns true if the node is either a seed node or a light node
func (n Node) Stateless() bool {
	return n.Mode == ModeLight || n.Mode == ModeSeed
}

// keyGenerator generates pseudorandom Ed25519 keys based on a seed.
type keyGenerator struct {
	random *rand.Rand
}

func newKeyGenerator(seed int64) *keyGenerator {
	return &keyGenerator{
		random: rand.New(rand.NewSource(seed)), //nolint:gosec
	}
}

func (g *keyGenerator) Generate(keyType string) crypto.PrivKey {
	seed := make([]byte, ed25519.SeedSize)

	_, err := io.ReadFull(g.random, seed)
	if err != nil {
		panic(err) // this shouldn't happen
	}
	switch keyType {
	case "secp256k1":
		return secp256k1.GenPrivKeySecp256k1(seed)
	case "sr25519":
		return sr25519.GenPrivKeyFromSecret(seed)
	case "", "ed25519":
		return ed25519.GenPrivKeyFromSecret(seed)
	default:
		panic("KeyType not supported") // should not make it this far
	}
}

// portGenerator generates local Docker proxy ports for each node.
type portGenerator struct {
	nextPort uint32
}

func newPortGenerator(firstPort uint32) *portGenerator {
	return &portGenerator{nextPort: firstPort}
}

func (g *portGenerator) Next() uint32 {
	port := g.nextPort
	g.nextPort++
	if g.nextPort == 0 {
		panic("port overflow")
	}
	return port
}

// ipGenerator generates sequential IP addresses for each node, using a random
// network address.
type ipGenerator struct {
	network *net.IPNet
	nextIP  net.IP
}

func newIPGenerator(network *net.IPNet) *ipGenerator {
	nextIP := make([]byte, len(network.IP))
	copy(nextIP, network.IP)
	gen := &ipGenerator{network: network, nextIP: nextIP}
	// Skip network and gateway addresses
	gen.Next()
	gen.Next()
	return gen
}

func (g *ipGenerator) Network() *net.IPNet {
	n := &net.IPNet{
		IP:   make([]byte, len(g.network.IP)),
		Mask: make([]byte, len(g.network.Mask)),
	}
	copy(n.IP, g.network.IP)
	copy(n.Mask, g.network.Mask)
	return n
}

func (g *ipGenerator) Next() net.IP {
	ip := make([]byte, len(g.nextIP))
	copy(ip, g.nextIP)
	for i := len(g.nextIP) - 1; i >= 0; i-- {
		g.nextIP[i]++
		if g.nextIP[i] != 0 {
			break
		}
	}
	return ip
}
