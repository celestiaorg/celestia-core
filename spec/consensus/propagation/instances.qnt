// -*- mode: Bluespec; -*-
//
// Configurations of propagation.qnt. Each module fixes the constants; pass
// one as --main. TRACE = false instances are for exhaustive checking (TLC):
// lastAction and the logs stay constant there, which does not change the
// behaviour of the other variables (no guard reads them). *Traced instances
// record them, for simulation, tests and ITF traces.

// ab6a3df applied (the code on main): `make verify` checks S1, S2 and
// S3_decision. The safety check is
// split in two exhaustive configurations (see README, "Reductions"):
//   fixedIdentity: byzantine proposer (A, B, X; commitments for A or B), no
//                  parts: forwarding, identity and channel/lock logic.
//   fixedParts:    honest proposer, parts and mempool recovery on: part
//                  sends, decoding and the blocking they cause.
module fixedIdentity {
  import propagation(
    K = 4, MEMPOOL = 0, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = true, ASYNC_DELIVERY = true, TRACE = false, TRACK_REQUESTS = false, PARTS = false, LOG_CAP = 4
  ).* from "./propagation"
}

module fixedParts {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A"), COMMITS = Set("A"), VOTES = true,
    FORWARD_ONCE = true, ASYNC_DELIVERY = true, TRACE = false, TRACK_REQUESTS = false, PARTS = true, LOG_CAP = 4
  ).* from "./propagation"
}

// the same two with channel capacity 2.
module fixedIdentityCap2 {
  import propagation(
    K = 4, MEMPOOL = 0, CAP_PROP = 2, CAP_PART = 2, CAP_PMQ = 2, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = true, ASYNC_DELIVERY = true, TRACE = false, TRACK_REQUESTS = false, PARTS = false, LOG_CAP = 4
  ).* from "./propagation"
}

module fixedPartsCap2 {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 2, CAP_PART = 2, CAP_PMQ = 2, MAX_ROUND = 1,
    BLOCKS = Set("A"), COMMITS = Set("A"), VOTES = true,
    FORWARD_ONCE = true, ASYNC_DELIVERY = true, TRACE = false, TRACK_REQUESTS = false, PARTS = true, LOG_CAP = 4
  ).* from "./propagation"
}

// everything at once (byzantine proposer and parts): too large to exhaust on
// 4 cores; used for simulation and bounded checks only.
module fixed {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = true, ASYNC_DELIVERY = true, TRACE = false, TRACK_REQUESTS = false, PARTS = true, LOG_CAP = 4
  ).* from "./propagation"
}

// ab6a3df reverted (both halves): the gate expects S1 and S2 violations.
module prefix {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = false, ASYNC_DELIVERY = false, TRACE = false, TRACK_REQUESTS = false, PARTS = true, LOG_CAP = 4
  ).* from "./propagation"
}

// only half (a) applied: forward once, delivery still on the receive routine.
module forwardOnceOnly {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = true, ASYNC_DELIVERY = false, TRACE = false, TRACK_REQUESTS = false, PARTS = true, LOG_CAP = 4
  ).* from "./propagation"
}

// only half (b) applied: async delivery, duplicates still forwarded.
module asyncOnly {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = false, ASYNC_DELIVERY = true, TRACE = false, TRACK_REQUESTS = false, PARTS = true, LOG_CAP = 4
  ).* from "./propagation"
}

// L1: honest proposer (only A is signed, only A is committed).
module live {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A"), COMMITS = Set("A"), VOTES = false,
    FORWARD_ONCE = true, ASYNC_DELIVERY = true, TRACE = false, TRACK_REQUESTS = true, PARTS = true, LOG_CAP = 4
  ).* from "./propagation"
}

// L1 with one round (MAX_ROUND = 0): small enough for every CI run.
module liveOneRound {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 0,
    BLOCKS = Set("A"), COMMITS = Set("A"), VOTES = false,
    FORWARD_ONCE = true, ASYNC_DELIVERY = true, TRACE = false, TRACK_REQUESTS = true, PARTS = true, LOG_CAP = 4
  ).* from "./propagation"
}

// traced twins, for quint run / quint test / ITF output
module fixedTraced {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = true, ASYNC_DELIVERY = true, TRACE = true, TRACK_REQUESTS = true, PARTS = true, LOG_CAP = 6
  ).* from "./propagation"
}

module prefixTraced {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = false, ASYNC_DELIVERY = false, TRACE = true, TRACK_REQUESTS = true, PARTS = true, LOG_CAP = 6
  ).* from "./propagation"
}

module forwardOnceOnlyTraced {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = true, ASYNC_DELIVERY = false, TRACE = true, TRACK_REQUESTS = true, PARTS = true, LOG_CAP = 6
  ).* from "./propagation"
}

module asyncOnlyTraced {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = false, ASYNC_DELIVERY = true, TRACE = true, TRACK_REQUESTS = true, PARTS = true, LOG_CAP = 6
  ).* from "./propagation"
}
