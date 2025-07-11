---
order: 3
---

# Configuration

CometBFT can be configured via a TOML file in
`$CMTHOME/config/config.toml`. Some of these parameters can be overridden by
command-line flags. For most users, the options in the `##### main base configuration options #####` are intended to be modified while config options
further below are intended for advance power users.

## Options

The default configuration file create by `cometbft init` has all
the parameters set with their default values. It will look something
like the file below, however, double check by inspecting the
`config.toml` created with your version of `cometbft` installed:

```toml
# This is a TOML config file.
# For more information, see https://github.com/toml-lang/toml

# NOTE: Any path below can be absolute (e.g. "/var/myawesomeapp/data") or
# relative to the home directory (e.g. "data"). The home directory is
# "$HOME/.cometbft" by default, but could be changed via $CMTHOME env variable
# or --home cmd flag.

# The version of the CometBFT binary that created or
# last modified the config file. Do not modify this.
version = "0.38.0"

#######################################################################
###                   Main Base Config Options                      ###
#######################################################################

# TCP or UNIX socket address of the ABCI application,
# or the name of an ABCI application compiled in with the CometBFT binary
proxy_app = "tcp://127.0.0.1:36658"

# A custom human readable name for this node
moniker = "anonymous"

# Database backend: goleveldb | cleveldb | boltdb | rocksdb | badgerdb
# * goleveldb (github.com/syndtr/goleveldb)
#   - UNMAINTAINED
#   - stable
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
db_backend = "goleveldb"

# Database directory
db_dir = "data"

# Blockstore directory. If not set, defaults to db_dir.
# This allows placing the blockstore on a separate volume (e.g. HDD)
# while keeping the state store on a faster volume (e.g. SSD).
#
# IMPORTANT: For existing nodes, manual data migration is required when changing
# this setting. To move your blockstore to a new location:
# 1. Stop your node
# 2. Move your current blockstore data (typically found in 'data/blockstore.db')
#    to the new location you intend to use for blockstore_dir
# 3. Update this setting to point to the new location
# 4. Restart your node
#
# WARNING: Setting this to a new empty directory on an existing node without
# moving the data will result in the node initializing a fresh blockstore,
# which will be inconsistent with your existing state database.
blockstore_dir = "data"

# Output level for logging, including package level options
log_level = "info"

# Output format: 'plain' (colored text) or 'json'
log_format = "plain"

##### additional base config options #####

# Path to the JSON file containing the initial validator set and other meta data
genesis_file = "config/genesis.json"

# Path to the JSON file containing the private key to use as a validator in the consensus protocol
priv_validator_key_file = "config/priv_validator_key.json"

# Path to the JSON file containing the last sign state of a validator
priv_validator_state_file = "data/priv_validator_state.json"

# TCP or UNIX socket address for CometBFT to listen on for
# connections from an external PrivValidator process
priv_validator_laddr = ""

# Path to the JSON file containing the private key to use for node authentication in the p2p protocol
node_key_file = "config/node_key.json"

# Mechanism to connect to the ABCI application: socket | grpc
abci = "socket"

# If true, query the ABCI app on connecting to a new peer
# so the app can decide if we should keep the connection or not
filter_peers = false


#######################################################################
###                 Advanced Configuration Options                  ###
#######################################################################

#######################################################
###       RPC Server Configuration Options          ###
#######################################################
[rpc]

# TCP or UNIX socket address for the RPC server to listen on
laddr = "tcp://127.0.0.1:26657"

# A list of origins a cross-domain request can be executed from
# Default value '[]' disables cors support
# Use '["*"]' to allow any origin
cors_allowed_origins = []

# A list of methods the client is allowed to use with cross-domain requests
cors_allowed_methods = ["HEAD", "GET", "POST", ]

# A list of non simple headers the client is allowed to use with cross-domain requests
cors_allowed_headers = ["Origin", "Accept", "Content-Type", "X-Requested-With", "X-Server-Time", ]

# TCP or UNIX socket address for the gRPC server to listen on
# NOTE: This server only supports /broadcast_tx_commit
grpc_laddr = ""

# Maximum number of simultaneous connections.
# Does not include RPC (HTTP&WebSocket) connections. See max_open_connections
# If you want to accept a larger number than the default, make sure
# you increase your OS limits.
# 0 - unlimited.
# Should be < {ulimit -Sn} - {MaxNumInboundPeers} - {MaxNumOutboundPeers} - {N of wal, db and other open files}
# 1024 - 40 - 10 - 50 = 924 = ~900
grpc_max_open_connections = 900

# Activate unsafe RPC commands like /dial_seeds and /unsafe_flush_mempool
unsafe = false

# Maximum number of simultaneous connections (including WebSocket).
# Does not include gRPC connections. See grpc_max_open_connections
# If you want to accept a larger number than the default, make sure
# you increase your OS limits.
# 0 - unlimited.
# Should be < {ulimit -Sn} - {MaxNumInboundPeers} - {MaxNumOutboundPeers} - {N of wal, db and other open files}
# 1024 - 40 - 10 - 50 = 924 = ~900
max_open_connections = 900

# Maximum number of unique clientIDs that can /subscribe
# If you're using /broadcast_tx_commit, set to the estimated maximum number
# of broadcast_tx_commit calls per block.
max_subscription_clients = 100

# Maximum number of unique queries a given client can /subscribe to
# If you're using GRPC (or Local RPC client) and /broadcast_tx_commit, set to
# the estimated # maximum number of broadcast_tx_commit calls per block.
max_subscriptions_per_client = 5

# Experimental parameter to specify the maximum number of events a node will
# buffer, per subscription, before returning an error and closing the
# subscription. Must be set to at least 100, but higher values will accommodate
# higher event throughput rates (and will use more memory).
experimental_subscription_buffer_size = 200

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
experimental_websocket_write_buffer_size = 200

# If a WebSocket client cannot read fast enough, at present we may
# silently drop events instead of generating an error or disconnecting the
# client.
#
# Enabling this experimental parameter will cause the WebSocket connection to
# be closed instead if it cannot read fast enough, allowing for greater
# predictability in subscription behavior.
experimental_close_on_slow_client = false

# How long to wait for a tx to be committed during /broadcast_tx_commit.
# WARNING: Using a value larger than 10s will result in increasing the
# global HTTP write timeout, which applies to all connections and endpoints.
# See https://github.com/tendermint/tendermint/issues/3435
timeout_broadcast_tx_commit = "10s"

# Maximum number of requests that can be sent in a JSON-RPC batch request.
# Possible values: number greater than 0.
# If the number of requests sent in a JSON-RPC batch exceed the maximum batch
# size configured, an error will be returned.
# The default value is set to `10`, which will limit the number of requests
# to 10 requests per a JSON-RPC batch request.
# If you don't want to enforce a maximum number of requests for a batch
# request set this value to `0`.
max_request_batch_size = 10

# Maximum size of request body, in bytes
max_body_bytes = 1000000

# Maximum size of request header, in bytes
max_header_bytes = 1048576

# The path to a file containing certificate that is used to create the HTTPS server.
# Might be either absolute path or path related to CometBFT's config directory.
# If the certificate is signed by a certificate authority,
# the certFile should be the concatenation of the server's certificate, any intermediates,
# and the CA's certificate.
# NOTE: both tls_cert_file and tls_key_file must be present for CometBFT to create HTTPS server.
# Otherwise, HTTP server is run.
tls_cert_file = ""

# The path to a file containing matching private key that is used to create the HTTPS server.
# Might be either absolute path or path related to CometBFT's config directory.
# NOTE: both tls-cert-file and tls-key-file must be present for CometBFT to create HTTPS server.
# Otherwise, HTTP server is run.
tls_key_file = ""

# pprof listen address (https://golang.org/pkg/net/http/pprof)
pprof_laddr = ""

#######################################################
###           P2P Configuration Options             ###
#######################################################
[p2p]

# Address to listen for incoming connections
laddr = "tcp://0.0.0.0:26656"

# Address to advertise to peers for them to dial. If empty, will use the same
# port as the laddr, and will introspect on the listener to figure out the
# address. IP and port are required. Example: 159.89.10.97:26656
external_address = ""

# Comma separated list of seed nodes to connect to
seeds = ""

# Comma separated list of nodes to keep persistent connections to
persistent_peers = ""

# Path to address book
addr_book_file = "config/addrbook.json"

# Set true for strict address routability rules
# Set false for private or local networks
addr_book_strict = true

# Maximum number of inbound peers
max_num_inbound_peers = 40

# Maximum number of outbound peers to connect to, excluding persistent peers
max_num_outbound_peers = 10

# List of node IDs, to which a connection will be (re)established ignoring any existing limits
unconditional_peer_ids = ""

# Maximum pause when redialing a persistent peer (if zero, exponential backoff is used)
persistent_peers_max_dial_period = "0s"

# Time to wait before flushing messages out on the connection
flush_throttle_timeout = "100ms"

# Maximum size of a message packet payload, in bytes
max_packet_msg_payload_size = 1024

# Rate at which packets can be sent, in bytes/second
send_rate = 5120000

# Rate at which packets can be received, in bytes/second
recv_rate = 5120000

# Set true to enable the peer-exchange reactor
pex = true

# Seed mode, in which node constantly crawls the network and looks for
# peers. If another node asks it for addresses, it responds and disconnects.
#
# Does not work if the peer-exchange reactor is disabled.
seed_mode = false

# Comma separated list of peer IDs to keep private (will not be gossiped to other peers)
private_peer_ids = ""

# Toggle to disable guard against peers connecting from the same ip.
allow_duplicate_ip = false

# Peer connection configuration.
handshake_timeout = "20s"
dial_timeout = "3s"

#######################################################
###          Mempool Configuration Option          ###
#######################################################
[mempool]

# The type of mempool for this node to use.
#
#  Possible types:
#  - "flood" : concurrent linked list mempool with flooding gossip protocol
#  (default)
#  - "nop"   : nop-mempool (short for no operation; the ABCI app is responsible
#  for storing, disseminating and proposing txs). "create_empty_blocks=false" is
#  not supported.
type = "flood"

# Recheck (default: true) defines whether CometBFT should recheck the
# validity for all remaining transaction in the mempool after a block.
# Since a block affects the application state, some transactions in the
# mempool may become invalid. If this does not apply to your application,
# you can disable rechecking.
recheck = true

# Broadcast (default: true) defines whether the mempool should relay
# transactions to other peers. Setting this to false will stop the mempool
# from relaying transactions to other peers until they are included in a
# block. In other words, if Broadcast is disabled, only the peer you send
# the tx to will see it until it is included in a block.
broadcast = true

# WalPath (default: "") configures the location of the Write Ahead Log
# (WAL) for the mempool. The WAL is disabled by default. To enable, set
# wal_dir to where you want the WAL to be written (e.g.
# "data/mempool.wal").
wal_dir = ""

# Maximum number of transactions in the mempool
size = 5000

# Limit the total size of all txs in the mempool.
# This only accounts for raw transactions (e.g. given 1MB transactions and
# max_txs_bytes=5MB, mempool will only accept 5 transactions).
max_txs_bytes = 1073741824

# Size of the cache (used to filter transactions we saw earlier) in transactions
cache_size = 10000

# Do not remove invalid transactions from the cache (default: false)
# Set to true if it's not possible for any invalid transaction to become valid
# again in the future.
keep-invalid-txs-in-cache = false

# Maximum size of a single transaction.
# NOTE: the max size of a tx transmitted over the network is {max_tx_bytes}.
max_tx_bytes = 1048576

# Maximum size of a batch of transactions to send to a peer
# Including space needed by encoding (one varint per transaction).
# XXX: Unused due to https://github.com/tendermint/tendermint/issues/5796
max_batch_bytes = 0

#######################################################
###         State Sync Configuration Options        ###
#######################################################
[statesync]
# State sync rapidly bootstraps a new node by discovering, fetching, and restoring a state machine
# snapshot from peers instead of fetching and replaying historical blocks. Requires some peers in
# the network to take and serve state machine snapshots. State sync is not attempted if the node
# has any local state (LastBlockHeight > 0). The node will have a truncated block history,
# starting from the height of the snapshot.
enable = false

# RPC servers (comma-separated) for light client verification of the synced state machine and
# retrieval of state data for node bootstrapping. Also needs a trusted height and corresponding
# header hash obtained from a trusted source, and a period during which validators can be trusted.
#
# For Cosmos SDK-based chains, trust_period should usually be about 2/3 of the unbonding time (~2
# weeks) during which they can be financially punished (slashed) for misbehavior.
rpc_servers = ""
trust_height = 0
trust_hash = ""
trust_period = "168h0m0s"

# Time to spend discovering snapshots before initiating a restore.
discovery_time = "15s"

# Temporary directory for state sync snapshot chunks, defaults to the OS tempdir (typically /tmp).
# Will create a new, randomly named directory within, and remove it when done.
temp_dir = ""

# The timeout duration before re-requesting a chunk, possibly from a different
# peer (default: 1 minute).
chunk_request_timeout = "10s"

# The number of concurrent chunk fetchers to run (default: 1).
chunk_fetchers = "4"

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
version = "v0"

#######################################################
###         Consensus Configuration Options         ###
#######################################################
[consensus]

wal_file = "data/cs.wal/wal"

# How long we wait for a proposal block before prevoting nil
timeout_propose = "3s"
# How much timeout_propose increases with each round
timeout_propose_delta = "500ms"
# How long we wait after receiving +2/3 prevotes for “anything” (ie. not a single block or nil)
timeout_prevote = "1s"
# How much the timeout_prevote increases with each round
timeout_prevote_delta = "500ms"
# How long we wait after receiving +2/3 precommits for “anything” (ie. not a single block or nil)
timeout_precommit = "1s"
# How much the timeout_precommit increases with each round
timeout_precommit_delta = "500ms"
# How long we wait after committing a block, before starting on the new
# height (this gives us a chance to receive some more precommits, even
# though we already have +2/3).
timeout_commit = "1s"

# How many blocks to look back to check existence of the node's consensus votes before joining consensus
# When non-zero, the node will panic upon restart
# if the same consensus key was used to sign {double_sign_check_height} last blocks.
# So, validators should stop the state machine, wait for some blocks, and then restart the state machine to avoid panic.
double_sign_check_height = 0

# Make progress as soon as we have all the precommits (as if TimeoutCommit = 0)
skip_timeout_commit = false

# EmptyBlocks mode and possible interval between empty blocks
create_empty_blocks = true
create_empty_blocks_interval = "0s"

# Reactor sleep duration parameters
peer_gossip_sleep_duration = "100ms"
peer_query_maj23_sleep_duration = "2s"

#######################################################
###         Storage Configuration Options           ###
#######################################################
[storage]

# Set to true to discard ABCI responses from the state store, which can save a
# considerable amount of disk space. Set to false to ensure ABCI responses are
# persisted. ABCI responses are required for /block_results RPC queries, and to
# reindex events in the command-line tool.
discard_abci_responses = false

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
#   1) "null"
#   2) "kv" (default) - the simplest possible indexer, backed by key-value storage (defaults to levelDB; see DBBackend).
# 		- When "kv" is chosen "tx.height" and "tx.hash" will always be indexed.
#   3) "psql" - the indexer services backed by PostgreSQL.
# When "kv" or "psql" is chosen "tx.height" and "tx.hash" will always be indexed.
indexer = "kv"

# The PostgreSQL connection configuration, the connection format:
#   postgresql://<user>:<password>@<host>:<port>/<db>?<opts>
psql-conn = ""

#######################################################
###       Instrumentation Configuration Options     ###
#######################################################
[instrumentation]

# When true, Prometheus metrics are served under /metrics on
# PrometheusListenAddr.
# Check out the documentation for the list of available metrics.
prometheus = false

# Address to listen for Prometheus collector(s) connections
prometheus_listen_addr = ":26660"

# Maximum number of simultaneous connections.
# If you want to accept a larger number than the default, make sure
# you increase your OS limits.
# 0 - unlimited.
max_open_connections = 3

# Instrumentation namespace
namespace = "cometbft"

 ```

## Empty blocks VS no empty blocks

### create_empty_blocks = true

If `create_empty_blocks` is set to `true` in your config, blocks will be created ~ every second (with default consensus parameters). You can regulate the delay between blocks by changing the `timeout_commit`. E.g. `timeout_commit = "10s"` should result in ~ 10 second blocks.

### create_empty_blocks = false

In this setting, blocks are created when transactions received.

Note after the block H, CometBFT creates something we call a "proof block" (only if the application hash changed) H+1. The reason for this is to support proofs. If you have a transaction in block H that changes the state to X, the new application hash will only be included in block H+1. If after your transaction is committed, you want to get a light-client proof for the new state (X), you need the new block to be committed in order to do that because the new block has the new application hash for the state X. That's why we make a new (empty) block if the application hash changes. Otherwise, you won't be able to make a proof for the new state.

Plus, if you set `create_empty_blocks_interval` to something other than the default (`0`), CometBFT will be creating empty blocks even in the absence of transactions every `create_empty_blocks_interval.` For instance, with `create_empty_blocks = false` and `create_empty_blocks_interval = "30s"`, CometBFT will only create blocks if there are transactions, or after waiting 30 seconds without receiving any transactions.

## Consensus timeouts explained

There's a variety of information about timeouts in [Running in
production](./running-in-production.md#configuration-parameters).
You can also find more detailed explanation in the paper describing
the Tendermint consensus algorithm, adopted by CometBFT: [The latest
gossip on BFT consensus](https://arxiv.org/abs/1807.04938).

```toml
[consensus]
...

timeout_propose = "3s"
timeout_propose_delta = "500ms"
timeout_prevote = "1s"
timeout_prevote_delta = "500ms"
timeout_precommit = "1s"
timeout_precommit_delta = "500ms"
timeout_commit = "1s"
```

Note that in a successful round, the only timeout that we absolutely wait no
matter what is `timeout_commit`.
Here's a brief summary of the timeouts:

- `timeout_propose` = how long a validator should wait for a proposal block before prevoting nil
- `timeout_propose_delta` = how much `timeout_propose` increases with each round
- `timeout_prevote` = how long a validator should wait after receiving +2/3 prevotes for
  anything (ie. not a single block or nil)
- `timeout_prevote_delta` = how much the `timeout_prevote` increases with each round
- `timeout_precommit` = how long a validator should wait after receiving +2/3 precommits for
  anything (ie. not a single block or nil)
- `timeout_precommit_delta` = how much the `timeout_precommit` increases with each round
- `timeout_commit` = how long a validator should wait after committing a block, before starting
  on the new height (this gives us a chance to receive some more precommits,
  even though we already have +2/3)

### The adverse effect of using inconsistent `timeout_propose` in a network

Here's an interesting question. What happens if a particular validator sets a
very small `timeout_propose`, as compared to the rest of the network?

Imagine there are only two validators in your network: Alice and Bob. Bob sets
`timeout_propose` to 0s. Alice uses the default value of 3s. Let's say they
both have an equal voting power. Given the proposer selection algorithm is a
weighted round-robin, you may expect Alice and Bob to take turns proposing
blocks, and the result like:

```
#1 block - Alice
#2 block - Bob
#3 block - Alice
#4 block - Bob
...
```

What happens in reality is, however, a little bit different:

```
#1 block - Bob
#2 block - Bob
#3 block - Bob
#4 block - Bob
```

That's because Bob doesn't wait for a proposal from Alice (prevotes `nil`).
This leaves Alice no chances to commit a block. Note that every block Bob
creates needs a vote from Alice to constitute 2/3+. Bob always gets one because
Alice has `timeout_propose` set to 3s. Alice never gets one because Bob has it
set to 0s.

Imagine now there are ten geographically distributed validators. One of them
(Bob) sets `timeout_propose` to 0s. Others have it set to 3s. Now, Bob won't be
able to move with his own speed because it still needs 2/3 votes of the other
validators and it takes time to propagate those. I.e., the network moves with
the speed of time to accumulate 2/3+ of votes (prevotes & precommits), not with
the speed of the fastest proposer.

> Isn't block production determined by voting power?

If it were determined solely by voting power, it wouldn't be possible to ensure
liveness. Timeouts exist because the network can't rely on a single proposer
being available and must move on if such is not responding.

> How can we address situations where someone arbitrarily adjusts their block
> production time to gain an advantage?

The impact shown above is negligible in a decentralized network with enough
decentralization.

### The adverse effect of using inconsistent `timeout_commit` in a network

Let's look at the same scenario as before. There are ten geographically
distributed validators. One of them (Bob) sets `timeout_commit` to 0s. Others
have it set to 1s (the default value). Now, Bob will be the fastest producer
because he doesn't wait for additional precommits after creating a block. If
waiting for precommits (`timeout_commit`) is not incentivized, Bob will accrue
more rewards compared to the other 9 validators.

This is because Bob has the advantage of broadcasting its proposal early (1
second earlier than the others). But it also makes it possible for Bob to miss
a proposal from another validator and prevote `nil` due to him starting
`timeout_propose` earlier. I.e., if Bob's `timeout_commit` is too low comparing
to other validators, then he might miss some proposals and get slashed for
inactivity.
