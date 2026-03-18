- Enforce `maxRequestsPerPeer` on the direct SeenTx→requestTx path in the
  CAT mempool reactor. Previously, a peer could bypass the 30-request per-peer
  cap by sending SeenTx messages without signer/sequence information.
