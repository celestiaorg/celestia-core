package e2e

import (
	"fmt"
	"net"
	"sort"
)

const (
	dockerIPv4CIDR = "10.186.73.0/24"
	dockerIPv6CIDR = "fd80:b10c::/48"

	globalIPv4CIDR = "0.0.0.0/0"
)

// InfrastructureData contains the relevant information for a set of existing
// infrastructure that is to be used for running a testnet.
type InfrastructureData struct {
	Path string

	// Provider is the name of infrastructure provider backing the testnet.
	// Currently, only 'docker' is supported.
	Provider string `json:"provider"`

	// Instances is a map of all of the machine instances on which to run
	// processes for a testnet.
	// The key of the map is the name of the instance, which each must correspond
	// to the names of one of the testnet nodes defined in the testnet manifest.
	Instances map[string]InstanceData `json:"instances"`

	// Network is the CIDR notation range of IP addresses that all of the instances'
	// IP addresses are expected to be within.
	Network string `json:"network"`

	// TracePushConfig is the URL of the server to push trace data to.
	TracePushConfig string `json:"trace_push_config,omitempty"`

	// TracePullAddress is the address to listen on for pulling trace data.
	TracePullAddress string `json:"trace_pull_address,omitempty"`

	// PyroscopeURL is the URL of the pyroscope instance to use for continuous
	// profiling. If not specified, data will not be collected.
	PyroscopeURL string `json:"pyroscope_url,omitempty"`

	// PyroscopeTrace enables adding trace data to pyroscope profiling.
	PyroscopeTrace bool `json:"pyroscope_trace,omitempty"`

	// PyroscopeProfileTypes is the list of profile types to collect.
	PyroscopeProfileTypes []string `json:"pyroscope_profile_types,omitempty"`
}

// InstanceData contains the relevant information for a machine instance backing
// one of the nodes in the testnet.
type InstanceData struct {
	IPAddress    net.IP `json:"ip_address"`
	ExtIPAddress net.IP `json:"ext_ip_address"`
	Port         uint32 `json:"port"`
}

func sortNodeNames(m Manifest) []string {
	// Set up nodes, in alphabetical order (IPs and ports get same order).
	nodeNames := []string{}
	for name := range m.Nodes {
		nodeNames = append(nodeNames, name)
	}
	sort.Strings(nodeNames)
	return nodeNames
}

func NewDockerInfrastructureData(m Manifest) (InfrastructureData, error) {
	netAddress := dockerIPv4CIDR
	if m.IPv6 {
		netAddress = dockerIPv6CIDR
	}
	_, ipNet, err := net.ParseCIDR(netAddress)
	if err != nil {
		return InfrastructureData{}, fmt.Errorf("invalid IP network address %q: %w", netAddress, err)
	}

	portGen := newPortGenerator(proxyPortFirst)
	ipGen := newIPGenerator(ipNet)
	ifd := InfrastructureData{
		Provider:  "docker",
		Instances: make(map[string]InstanceData),
		Network:   netAddress,
	}
	localHostIP := net.ParseIP("127.0.0.1")
	for _, name := range sortNodeNames(m) {
		ifd.Instances[name] = InstanceData{
			IPAddress:    ipGen.Next(),
			ExtIPAddress: localHostIP,
			Port:         portGen.Next(),
		}

	}
	return ifd, nil
}
