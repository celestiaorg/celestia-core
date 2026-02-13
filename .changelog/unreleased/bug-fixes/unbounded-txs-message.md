Disconnect peers that send a Txs message with more than one transaction,
empty transactions, or an entirely empty Txs array. Transaction batching
was disabled in https://github.com/tendermint/tendermint/issues/5796 so
only a single transaction per message is expected. Previously, a
malicious peer could send hundreds of thousands of zero-length
transactions in a single message, causing unbounded CPU and memory
growth.
