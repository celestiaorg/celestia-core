- Cap `SeenTxSet` to 10,000,000 entries (~5 GB worst-case) to prevent
  unbounded memory growth from malicious peers flooding SeenTx messages
  with random tx keys.
  ([\#CELESTIA-256](https://github.com/celestiaorg/celestia-core/issues/CELESTIA-256))
