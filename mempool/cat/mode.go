package cat

import "os"

// rpcPushModeEnvVar is the environment variable that enables RPC push mode.
// When set to any non-empty value, transactions submitted via the local RPC
// (BroadcastTx*) are pushed as full Txs messages to every connected peer
// instead of being announced with SeenLargeTx and pulled back via the
// chunked path. Receiving peers admit the tx and then re-broadcast via the
// chunked mempool, so chunked propagation still drives peer-to-peer fan-out.
const rpcPushModeEnvVar = "RPC"

// RPCPushModeEnabled reports whether the operator has enabled RPC push mode by
// setting the RPC environment variable to a non-empty value.
func RPCPushModeEnabled() bool {
	return os.Getenv(rpcPushModeEnvVar) != ""
}
