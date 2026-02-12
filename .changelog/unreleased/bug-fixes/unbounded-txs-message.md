Disconnect peers that send a Txs message with too many transactions
(more than 64), empty transactions, or an entirely empty Txs array.
Previously, a malicious peer could send hundreds of thousands of
zero-length transactions in a single message, causing unbounded CPU
and memory growth.
