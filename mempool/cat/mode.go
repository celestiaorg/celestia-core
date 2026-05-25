package cat

import "os"

// rpcPushModeEnvVar is the environment variable that enables RPC push mode.
// When set to any non-empty value, transactions submitted via the local RPC
// (BroadcastTx*) are pushed as full Txs messages to every connected peer
// instead of being announced with SeenTx and pulled back via WantTx.
const rpcPushModeEnvVar = "RPC"

// RPCPushModeEnabled reports whether the operator has enabled RPC push mode by
// setting the RPC environment variable to a non-empty value.
func RPCPushModeEnabled() bool {
	return os.Getenv(rpcPushModeEnvVar) != ""
}
