package chunked

// AnnounceFanout returns the number of peers a SeenLargeTx should be sent to,
// given the chunk count of the announced tx and the configured target number
// of total advertisements per tx.
//
// Formula: fanout = max(1, ceil(target / num_parts)).
//
//   - 1-chunk tx, target=60   -> fanout 60 (no parity, so chunk-level
//     diversity is the only redundancy)
//   - 8-chunk tx, target=60   -> fanout 8
//   - 60-chunk tx, target=60  -> fanout 1
//   - 1024-chunk tx, target=60 -> fanout 1 (chunks self-distribute)
//
// target <= 0 falls back to DefaultAnnounceTarget. num_parts == 0 is treated
// as 1 to avoid divide-by-zero in callers that haven't yet validated.
func AnnounceFanout(numParts uint32, target int) int {
	if target <= 0 {
		target = DefaultAnnounceTarget
	}
	n := int(numParts)
	if n <= 0 {
		n = 1
	}
	f := (target + n - 1) / n
	if f < 1 {
		return 1
	}
	return f
}

// PerPeerInflightCap returns the per-peer concurrent-chunk-request limit for
// a given tx, mirroring the propagation reactor formula in
// consensus/propagation/reactor.go:376:
//
//	cap = ceil((numParts / 2) / ceil(connectedPeers * 0.33))
//
// The factor 0.33 reflects 1/3 byzantine resilience: even if up to a third
// of peers fail or are byzantine, the remainder can still serve enough
// chunks to reconstruct.
func PerPeerInflightCap(numParts uint32, connectedPeers int) int {
	if numParts == 0 {
		return 1
	}
	if connectedPeers < 1 {
		connectedPeers = 1
	}
	denom := (connectedPeers*33 + 99) / 100 // ceil(connectedPeers * 0.33)
	if denom < 1 {
		denom = 1
	}
	half := (int(numParts) + 1) / 2 // ceil(numParts / 2)
	cap := (half + denom - 1) / denom
	if cap < 1 {
		return 1
	}
	return cap
}
