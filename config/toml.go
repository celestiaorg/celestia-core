package config

import (
	"bytes"
	"path/filepath"
	"strings"
	"text/template"

	cmtos "github.com/cometbft/cometbft/libs/os"
)

// DefaultDirPerm is the default permissions used when creating directories.
const DefaultDirPerm = 0700

var configTemplate *template.Template

func init() {
	var err error
	tmpl := template.New("configFileTemplate").Funcs(template.FuncMap{
		"StringsJoin": strings.Join,
	})
	if configTemplate, err = tmpl.Parse(defaultConfigTemplate); err != nil {
		panic(err)
	}
}

/****** these are for production settings ***********/

// EnsureRoot creates the root, config, and data directories if they don't exist,
// and panics if it fails.
func EnsureRoot(rootDir string) {
	if err := cmtos.EnsureDir(rootDir, DefaultDirPerm); err != nil {
		panic(err.Error())
	}
	if err := cmtos.EnsureDir(filepath.Join(rootDir, DefaultConfigDir), DefaultDirPerm); err != nil {
		panic(err.Error())
	}
	if err := cmtos.EnsureDir(filepath.Join(rootDir, DefaultDataDir), DefaultDirPerm); err != nil {
		panic(err.Error())
	}

	configFilePath := filepath.Join(rootDir, defaultConfigFilePath)

	// Write default config file if missing.
	if !cmtos.FileExists(configFilePath) {
		writeDefaultConfigFile(configFilePath)
	}
}

// XXX: this func should probably be called by cmd/cometbft/commands/init.go
// alongside the writing of the genesis.json and priv_validator.json
func writeDefaultConfigFile(configFilePath string) {
	WriteConfigFile(configFilePath, DefaultConfig())
}

// WriteConfigFile renders config using the template and writes it to configFilePath.
func WriteConfigFile(configFilePath string, config *Config) {
	var buffer bytes.Buffer

	if err := configTemplate.Execute(&buffer, config); err != nil {
		panic(err)
	}

	cmtos.MustWriteFile(configFilePath, buffer.Bytes(), 0644)
}

// Note: any changes to the comments/variables/mapstructure
// must be reflected in the appropriate struct in config/config.go
const defaultConfigTemplate = `# This is a TOML config file.
# For more information, see https://github.com/toml-lang/toml

# NOTE: Any path below can be absolute (e.g. "/var/myawesomeapp/data") or
# relative to the home directory (e.g. "data"). The home directory is
# "$HOME/.cometbft" by default, but could be changed via $CMTHOME env variable
# or --home cmd flag.

# The version of the CometBFT binary that created or
# last modified the config file. Do not modify this.
version = "{{ .BaseConfig.Version }}"

#######################################################################
###                   Main Base Config Options                      ###
#######################################################################

# TCP or UNIX socket address of the ABCI application,
# or the name of an ABCI application compiled in with the CometBFT binary
proxy_app = "{{ .BaseConfig.ProxyApp }}"

# A custom human readable name for this node
moniker = "{{ .BaseConfig.Moniker }}"

# Database backend: goleveldb | cleveldb | boltdb | rocksdb | badgerdb
# * goleveldb (github.com/syndtr/goleveldb - most popular implementation)
#   - pure go
#   - stable
# * cleveldb (uses levigo wrapper)
#   - fast
#   - requires gcc
#   - use cleveldb build tag (go build -tags cleveldb)
# * boltdb (uses etcd's fork of bolt - github.com/etcd-io/bbolt)
#   - EXPERIMENTAL
#   - may be faster is some use-cases (random reads - indexer)
#   - use boltdb build tag (go build -tags boltdb)
# * rocksdb (uses github.com/tecbot/gorocksdb)
#   - EXPERIMENTAL
#   - requires gcc
#   - use rocksdb build tag (go build -tags rocksdb)
# * badgerdb (uses github.com/dgraph-io/badger)
#   - EXPERIMENTAL
#   - use badgerdb build tag (go build -tags badgerdb)
db_backend = "{{ .BaseConfig.DBBackend }}"

# Database directory
db_dir = "{{ js .BaseConfig.DBPath }}"

# Blockstore directory. If not set, defaults to db_dir
blockstore_dir = "{{ js .BaseConfig.BlockstorePath }}"

# Output level for logging, including package level options
log_level = "{{ .BaseConfig.LogLevel }}"

# Output format: 'plain' (colored text) or 'json'
log_format = "{{ .BaseConfig.LogFormat }}"

##### additional base config options #####

# Path to the JSON file containing the initial validator set and other meta data
genesis_file = "{{ js .BaseConfig.Genesis }}"

# Path to the JSON file containing the private key to use as a validator in the consensus protocol
priv_validator_key_file = "{{ js .BaseConfig.PrivValidatorKey }}"

# Path to the JSON file containing the last sign state of a validator
priv_validator_state_file = "{{ js .BaseConfig.PrivValidatorState }}"

# TCP or UNIX socket address for CometBFT to listen on for
# connections from an external PrivValidator process
priv_validator_laddr = "{{ .BaseConfig.PrivValidatorListenAddr }}"

# Path to the JSON file containing the private key to use for node authentication in the p2p protocol
node_key_file = "{{ js .BaseConfig.NodeKey }}"

# Mechanism to connect to the ABCI application: socket | grpc
abci = "{{ .BaseConfig.ABCI }}"

# If true, query the ABCI app on connecting to a new peer
# so the app can decide if we should keep the connection or not
filter_peers = {{ .BaseConfig.FilterPeers }}


#######################################################################
###                 Advanced Configuration Options                  ###
#######################################################################

#######################################################
###       RPC Server Configuration Options          ###
#######################################################
[rpc]

# TCP or UNIX socket address for the RPC server to listen on
laddr = "{{ .RPC.ListenAddress }}"

# A list of origins a cross-domain request can be executed from
# Default value '[]' disables cors support
# Use '["*"]' to allow any origin
cors_allowed_origins = [{{ range .RPC.CORSAllowedOrigins }}{{ printf "%q, " . }}{{end}}]

# A list of methods the client is allowed to use with cross-domain requests
cors_allowed_methods = [{{ range .RPC.CORSAllowedMethods }}{{ printf "%q, " . }}{{end}}]

# A list of non simple headers the client is allowed to use with cross-domain requests
cors_allowed_headers = [{{ range .RPC.CORSAllowedHeaders }}{{ printf "%q, " . }}{{end}}]

# TCP or UNIX socket address for the gRPC server to listen on
grpc_laddr = "{{ .RPC.GRPCListenAddress }}"

# Maximum number of simultaneous connections.
# Does not include RPC (HTTP&WebSocket) connections. See max_open_connections
# If you want to accept a larger number than the default, make sure
# you increase your OS limits.
# 0 - unlimited.
# Should be < {ulimit -Sn} - {MaxNumInboundPeers} - {MaxNumOutboundPeers} - {N of wal, db and other open files}
# 1024 - 40 - 10 - 50 = 924 = ~900
grpc_max_open_connections = {{ .RPC.GRPCMaxOpenConnections }}

# Activate unsafe RPC commands like /dial_seeds and /unsafe_flush_mempool
unsafe = {{ .RPC.Unsafe }}

# Maximum number of simultaneous connections (including WebSocket).
# Does not include gRPC connections. See grpc_max_open_connections
# If you want to accept a larger number than the default, make sure
# you increase your OS limits.
# 0 - unlimited.
# Should be < {ulimit -Sn} - {MaxNumInboundPeers} - {MaxNumOutboundPeers} - {N of wal, db and other open files}
# 1024 - 40 - 10 - 50 = 924 = ~900
max_open_connections = {{ .RPC.MaxOpenConnections }}

# Maximum number of unique clientIDs that can /subscribe
# If you're using /broadcast_tx_commit, set to the estimated maximum number
# of broadcast_tx_commit calls per block.
max_subscription_clients = {{ .RPC.MaxSubscriptionClients }}

# Maximum number of unique queries a given client can /subscribe to
# If you're using GRPC (or Local RPC client) and /broadcast_tx_commit, set to
# the estimated # maximum number of broadcast_tx_commit calls per block.
max_subscriptions_per_client = {{ .RPC.MaxSubscriptionsPerClient }}

# Experimental parameter to specify the maximum number of events a node will
# buffer, per subscription, before returning an error and closing the
# subscription. Must be set to at least 100, but higher values will accommodate
# higher event throughput rates (and will use more memory).
experimental_subscription_buffer_size = {{ .RPC.SubscriptionBufferSize }}

# Experimental parameter to specify the maximum number of RPC responses that
# can be buffered per WebSocket client. If clients cannot read from the
# WebSocket endpoint fast enough, they will be disconnected, so increasing this
# parameter may reduce the chances of them being disconnected (but will cause
# the node to use more memory).
#
# Must be at least the same as "experimental_subscription_buffer_size",
# otherwise connections could be dropped unnecessarily. This value should
# ideally be somewhat higher than "experimental_subscription_buffer_size" to
# accommodate non-subscription-related RPC responses.
experimental_websocket_write_buffer_size = {{ .RPC.WebSocketWriteBufferSize }}

# If a WebSocket client cannot read fast enough, at present we may
# silently drop events instead of generating an error or disconnecting the
# client.
#
# Enabling this experimental parameter will cause the WebSocket connection to
# be closed instead if it cannot read fast enough, allowing for greater
# predictability in subscription behavior.
experimental_close_on_slow_client = {{ .RPC.CloseOnSlowClient }}

# How long to wait for a tx to be committed during /broadcast_tx_commit.
# WARNING: Using a value larger than 10s will result in increasing the
# global HTTP write timeout, which applies to all connections and endpoints.
# See https://github.com/tendermint/tendermint/issues/3435
timeout_broadcast_tx_commit = "{{ .RPC.TimeoutBroadcastTxCommit }}"

# Maximum number of requests that can be sent in a batch
# If the value is set to '0' (zero-value), then no maximum batch size will be
# enforced for a JSON-RPC batch request.
max_request_batch_size = {{ .RPC.MaxRequestBatchSize }}

# Maximum size of request body, in bytes
max_body_bytes = {{ .RPC.MaxBodyBytes }}

# Maximum size of request header, in bytes
max_header_bytes = {{ .RPC.MaxHeaderBytes }}

# The path to a file containing certificate that is used to create the HTTPS server.
# Might be either absolute path or path related to CometBFT's config directory.
# If the certificate is signed by a certificate authority,
# the certFile should be the concatenation of the server's certificate, any intermediates,
# and the CA's certificate.
# NOTE: both tls_cert_file and tls_key_file must be present for CometBFT to create HTTPS server.
# Otherwise, HTTP server is run.
tls_cert_file = "{{ .RPC.TLSCertFile }}"

# The path to a file containing matching private key that is used to create the HTTPS server.
# Might be either absolute path or path related to CometBFT's config directory.
# NOTE: both tls-cert-file and tls-key-file must be present for CometBFT to create HTTPS server.
# Otherwise, HTTP server is run.
tls_key_file = "{{ .RPC.TLSKeyFile }}"

# pprof listen address (https://golang.org/pkg/net/http/pprof)
pprof_laddr = "{{ .RPC.PprofListenAddress }}"

#######################################################
###           P2P Configuration Options             ###
#######################################################
[p2p]

# Address to listen for incoming connections
laddr = "{{ .P2P.ListenAddress }}"

# Address to advertise to peers for them to dial. If empty, will use the same
# port as the laddr, and will introspect on the listener to figure out the
# address. IP and port are required. Example: 159.89.10.97:26656
external_address = "{{ .P2P.ExternalAddress }}"

# Comma separated list of seed nodes to connect to
seeds = "{{ .P2P.Seeds }}"

# Comma separated list of nodes to keep persistent connections to
persistent_peers = "{{ .P2P.PersistentPeers }}"

# Path to address book
addr_book_file = "{{ js .P2P.AddrBook }}"

# Set true for strict address routability rules
# Set false for private or local networks
addr_book_strict = {{ .P2P.AddrBookStrict }}

# Maximum number of inbound peers
max_num_inbound_peers = {{ .P2P.MaxNumInboundPeers }}

# Maximum number of outbound peers to connect to, excluding persistent peers
max_num_outbound_peers = {{ .P2P.MaxNumOutboundPeers }}

# List of node IDs, to which a connection will be (re)established ignoring any existing limits
unconditional_peer_ids = "{{ .P2P.UnconditionalPeerIDs }}"

# Maximum pause when redialing a persistent peer (if zero, exponential backoff is used)
persistent_peers_max_dial_period = "{{ .P2P.PersistentPeersMaxDialPeriod }}"

# Time to wait before flushing messages out on the connection
flush_throttle_timeout = "{{ .P2P.FlushThrottleTimeout }}"

# Maximum size of a message packet payload, in bytes
max_packet_msg_payload_size = {{ .P2P.MaxPacketMsgPayloadSize }}

# Rate at which packets can be sent, in bytes/second
send_rate = {{ .P2P.SendRate }}

# Rate at which packets can be received, in bytes/second
recv_rate = {{ .P2P.RecvRate }}

# Set true to enable the peer-exchange reactor
pex = {{ .P2P.PexReactor }}

# Seed mode, in which node constantly crawls the network and looks for
# peers. If another node asks it for addresses, it responds and disconnects.
#
# Does not work if the peer-exchange reactor is disabled.
seed_mode = {{ .P2P.SeedMode }}

# Comma separated list of peer IDs to keep private (will not be gossiped to other peers)
private_peer_ids = "{{ .P2P.PrivatePeerIDs }}"

# Toggle to disable guard against peers connecting from the same ip.
allow_duplicate_ip = {{ .P2P.AllowDuplicateIP }}

# Peer connection configuration.
handshake_timeout = "{{ .P2P.HandshakeTimeout }}"
dial_timeout = "{{ .P2P.DialTimeout }}"

#######################################################
###          Mempool Configuration Option          ###
#######################################################
[mempool]

# The type of mempool for this node to use.
#
#  Possible types:
#  - "flood" : concurrent linked list mempool with flooding gossip protocol
#  - "nop"   : nop-mempool (short for no operation; the ABCI app is responsible
#  for storing, disseminating and proposing txs). "create_empty_blocks=false" is
#  not supported.
#  - "priority" : concurrent linked list mempool with priority queue (default)
#  - "cat"   : content addressable mempool
type = "{{ .Mempool.Type }}"

# Recheck (default: true) defines whether CometBFT should recheck the
# validity for all remaining transaction in the mempool after a block.
# Since a block affects the application state, some transactions in the
# mempool may become invalid. If this does not apply to your application,
# you can disable rechecking.
recheck = {{ .Mempool.Recheck }}

# recheck_timeout is the time the application has during the rechecking process
# to return CheckTx responses, once all requests have been sent. Responses that
# arrive after the timeout expires are discarded. It only applies to
# non-local ABCI clients and when recheck is enabled.
#
# The ideal value will strongly depend on the application. It could roughly be estimated as the
# average size of the mempool multiplied by the average time it takes the application to validate one
# transaction. We consider that the ABCI application runs in the same location as the CometBFT binary
# so that the recheck duration is not affected by network delays when making requests and receiving responses.
recheck_timeout = "{{ .Mempool.RecheckTimeout }}"

# Broadcast (default: true) defines whether the mempool should relay
# transactions to other peers. Setting this to false will stop the mempool
# from relaying transactions to other peers until they are included in a
# block. In other words, if Broadcast is disabled, only the peer you send
# the tx to will see it until it is included in a block.
broadcast = {{ .Mempool.Broadcast }}

# WalPath (default: "") configures the location of the Write Ahead Log
# (WAL) for the mempool. The WAL is disabled by default. To enable, set
# WalPath to where you want the WAL to be written (e.g.
# "data/mempool.wal").
wal_dir = "{{ js .Mempool.WalPath }}"

# Maximum number of transactions in the mempool
size = {{ .Mempool.Size }}

# Limit the total size of all txs in the mempool.
# This only accounts for raw transactions (e.g. given 1MB transactions and
# max_txs_bytes=5MB, mempool will only accept 5 transactions).
max_txs_bytes = {{ .Mempool.MaxTxsBytes }}

# Size of the cache (used to filter transactions we saw earlier) in transactions
cache_size = {{ .Mempool.CacheSize }}

# Do not remove invalid transactions from the cache (default: false)
# Set to true if it's not possible for any invalid transaction to become valid
# again in the future.
keep-invalid-txs-in-cache = {{ .Mempool.KeepInvalidTxsInCache }}

# Maximum size of a single transaction.
# NOTE: the max size of a tx transmitted over the network is {max_tx_bytes}.
max_tx_bytes = {{ .Mempool.MaxTxBytes }}

# Maximum size of a batch of transactions to send to a peer
# Including space needed by encoding (one varint per transaction).
# XXX: Unused due to https://github.com/tendermint/tendermint/issues/5796
max_batch_bytes = {{ .Mempool.MaxBatchBytes }}

# ttl-duration, if non-zero, defines the maximum amount of time a transaction
# can exist for in the mempool.
#
# Note, if ttl-num-blocks is also defined, a transaction will be removed if it
# has existed in the mempool at least ttl-num-blocks number of blocks or if it's
# insertion time into the mempool is beyond ttl-duration.
ttl-duration = "{{ .Mempool.TTLDuration }}"

# ttl-num-blocks, if non-zero, defines the maximum number of blocks a transaction
# can exist for in the mempool.
#
# Note, if ttl-duration is also defined, a transaction will be removed if it
# has existed in the mempool at least ttl-num-blocks number of blocks or if
# it's insertion time into the mempool is beyond ttl-duration.
ttl-num-blocks = {{ .Mempool.TTLNumBlocks }}

# Experimental parameters to limit gossiping txs to up to the specified number of peers.
# We use two independent upper values for persistent and non-persistent peers.
# Unconditional peers are not affected by this feature.
# If we are connected to more than the specified number of persistent peers, only send txs to
# ExperimentalMaxGossipConnectionsToPersistentPeers of them. If one of those
# persistent peers disconnects, activate another persistent peer.
# Similarly for non-persistent peers, with an upper limit of
# ExperimentalMaxGossipConnectionsToNonPersistentPeers.
# If set to 0, the feature is disabled for the corresponding group of peers, that is, the
# number of active connections to that group of peers is not bounded.
# For non-persistent peers, if enabled, a value of 10 is recommended based on experimental
# performance results using the default P2P configuration.
experimental_max_gossip_connections_to_persistent_peers = {{ .Mempool.ExperimentalMaxGossipConnectionsToPersistentPeers }}
experimental_max_gossip_connections_to_non_persistent_peers = {{ .Mempool.ExperimentalMaxGossipConnectionsToNonPersistentPeers }}

# max-gossip-delay is the maximum allotted time that the reactor expects a transaction to
# arrive before issuing a new request to a different peer
# Only applicable to the v2 / CAT mempool
# Default is 200ms
max-gossip-delay = "{{ .Mempool.MaxGossipDelay }}"

#######################################################
###         State Sync Configuration Options        ###
#######################################################
[statesync]
# State sync rapidly bootstraps a new node by discovering, fetching, and restoring a state machine
# snapshot from peers instead of fetching and replaying historical blocks. Requires some peers in
# the network to take and serve state machine snapshots. State sync is not attempted if the node
# has any local state (LastBlockHeight > 0). The node will have a truncated block history,
# starting from the height of the snapshot.
enable = {{ .StateSync.Enable }}

# RPC servers (comma-separated) for light client verification of the synced state machine and
# retrieval of state data for node bootstrapping. Also needs a trusted height and corresponding
# header hash obtained from a trusted source, and a period during which validators can be trusted.
#
# For Cosmos SDK-based chains, trust_period should usually be about 2/3 of the unbonding time (~2
# weeks) during which they can be financially punished (slashed) for misbehavior.
rpc_servers = "{{ StringsJoin .StateSync.RPCServers "," }}"
trust_height = {{ .StateSync.TrustHeight }}
trust_hash = "{{ .StateSync.TrustHash }}"
trust_period = "{{ .StateSync.TrustPeriod }}"

# Time to spend discovering snapshots before initiating a restore.
discovery_time = "{{ .StateSync.DiscoveryTime }}"

# Temporary directory for state sync snapshot chunks, defaults to the OS tempdir (typically /tmp).
# Will create a new, randomly named directory within, and remove it when done.
temp_dir = "{{ .StateSync.TempDir }}"

# The timeout duration before re-requesting a chunk, possibly from a different
# peer (default: 1 minute).
chunk_request_timeout = "{{ .StateSync.ChunkRequestTimeout }}"

# The number of concurrent chunk fetchers to run (default: 1).
chunk_fetchers = "{{ .StateSync.ChunkFetchers }}"

#######################################################
###       Block Sync Configuration Options          ###
#######################################################
[blocksync]

# Block Sync version to use:
#
# In v0.37, v1 and v2 of the block sync protocols were deprecated.
# Please use v0 instead.
#
#   1) "v0" - the default block sync implementation
version = "{{ .BlockSync.Version }}"

#######################################################
###         Consensus Configuration Options         ###
#######################################################
[consensus]

# If set to "true", only internal messages will be
# written to the WAL. External messages like votes, proposal,
# block parts, will not be written.
only_internal_wal = "{{ .Consensus.OnlyInternalWal }}"

wal_file = "{{ js .Consensus.WalPath }}"

# How long we wait for a proposal block before prevoting nil
timeout_propose = "{{ .Consensus.TimeoutPropose }}"
# How much timeout_propose increases with each round
timeout_propose_delta = "{{ .Consensus.TimeoutProposeDelta }}"
# How long we wait after receiving +2/3 prevotes for “anything” (ie. not a single block or nil)
timeout_prevote = "{{ .Consensus.TimeoutPrevote }}"
# How much the timeout_prevote increases with each round
timeout_prevote_delta = "{{ .Consensus.TimeoutPrevoteDelta }}"
# How long we wait after receiving +2/3 precommits for “anything” (ie. not a single block or nil)
timeout_precommit = "{{ .Consensus.TimeoutPrecommit }}"
# How much the timeout_precommit increases with each round
timeout_precommit_delta = "{{ .Consensus.TimeoutPrecommitDelta }}"
# How long we wait after committing a block, before starting on the new
# height (this gives us a chance to receive some more precommits, even
# though we already have +2/3).
timeout_commit = "{{ .Consensus.TimeoutCommit }}"

# How many blocks to look back to check existence of the node's consensus votes before joining consensus
# When non-zero, the node will panic upon restart
# if the same consensus key was used to sign {double_sign_check_height} last blocks.
# So, validators should stop the state machine, wait for some blocks, and then restart the state machine to avoid panic.
double_sign_check_height = {{ .Consensus.DoubleSignCheckHeight }}

# Make progress as soon as we have all the precommits (as if TimeoutCommit = 0)
skip_timeout_commit = {{ .Consensus.SkipTimeoutCommit }}

# EmptyBlocks mode and possible interval between empty blocks
create_empty_blocks = {{ .Consensus.CreateEmptyBlocks }}
create_empty_blocks_interval = "{{ .Consensus.CreateEmptyBlocksInterval }}"

# Reactor sleep duration parameters
peer_gossip_sleep_duration = "{{ .Consensus.PeerGossipSleepDuration }}"
peer_query_maj23_sleep_duration = "{{ .Consensus.PeerQueryMaj23SleepDuration }}"

# Disable the propagation reactor for block and proposal recovery
disable_propagation_reactor = {{ .Consensus.DisablePropagationReactor }}

#######################################################
###         Storage Configuration Options           ###
#######################################################
[storage]

# Set to true to discard ABCI responses from the state store, which can save a
# considerable amount of disk space. Set to false to ensure ABCI responses are
# persisted. ABCI responses are required for /block_results RPC queries, and to
# reindex events in the command-line tool.
discard_abci_responses = {{ .Storage.DiscardABCIResponses}}

#######################################################
###   Transaction Indexer Configuration Options     ###
#######################################################
[tx_index]

# What indexer to use for transactions
#
# The application will set which txs to index. In some cases a node operator will be able
# to decide which txs to index based on configuration set in the application.
#
# Options:
#   1) "null" (default) - index transaction status only
#   2) "kv" - index transactions using key-value storage based on DBBackend. "tx.height" and "tx.hash" will always be indexed.
#   3) "psql" - index transactions using PostgreSQL. "tx.height" and "tx.hash" will always be indexed.
indexer = "{{ .TxIndex.Indexer }}"

# The PostgreSQL connection configuration, the connection format:
#   postgresql://<user>:<password>@<host>:<port>/<db>?<opts>
psql-conn = "{{ .TxIndex.PsqlConn }}"

#######################################################
###       Instrumentation Configuration Options     ###
#######################################################
[instrumentation]

# When true, Prometheus metrics are served under /metrics on
# PrometheusListenAddr.
# Check out the documentation for the list of available metrics.
prometheus = {{ .Instrumentation.Prometheus }}

# Address to listen for Prometheus collector(s) connections
prometheus_listen_addr = "{{ .Instrumentation.PrometheusListenAddr }}"

# Maximum number of simultaneous connections.
# If you want to accept a larger number than the default, make sure
# you increase your OS limits.
# 0 - unlimited.
max_open_connections = {{ .Instrumentation.MaxOpenConnections }}

# Instrumentation namespace
namespace = "{{ .Instrumentation.Namespace }}"

# TracePushConfig is the relative path of the push config.
# This second config contains credentials for where and how often to
# push trace data to. For example, if the config is next to this config,
# it would be "push_config.json".
trace_push_config = "{{ .Instrumentation.TracePushConfig }}"

# The tracer pull address specifies which address will be used for pull based
# event collection. If empty, the pull based server will not be started.
trace_pull_address = "{{ .Instrumentation.TracePullAddress }}"

# The tracer to use for collecting trace data.
trace_type = "{{ .Instrumentation.TraceType }}"

# The size of the batches that are sent to the database.
trace_push_batch_size = {{ .Instrumentation.TraceBufferSize }}

# The list of tables that are updated when tracing. All available tables and
# their schema can be found in the pkg/trace/schema package. It is represented as a
# comma separate string. For example: "consensus_round_state,mempool_tx".
tracing_tables = "{{ .Instrumentation.TracingTables }}"

# The URL of the pyroscope instance to use for continuous profiling.
# If empty, continuous profiling is disabled.
pyroscope_url = "{{ .Instrumentation.PyroscopeURL }}"

# When true, tracing data is added to the continuous profiling
# performed by pyroscope.
pyroscope_trace = {{ .Instrumentation.PyroscopeTrace }}

# pyroscope_profile_types is a list of profile types to be traced with
# pyroscope. Available profile types are: cpu, alloc_objects, alloc_space,
# inuse_objects, inuse_space, goroutines, mutex_count, mutex_duration,
# block_count, block_duration.
pyroscope_profile_types = [{{ range .Instrumentation.PyroscopeProfileTypes }}{{ printf "%q, " . }}{{end}}]
`
