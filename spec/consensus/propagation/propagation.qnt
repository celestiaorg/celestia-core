// -*- mode: Bluespec; -*-
//
// Compact-block propagation <-> consensus hand-off (celestia-core).
//
// Scope: one height H, rounds 0..MAX_ROUND, one local node, a proposer key and
// two peers (p1, p2). The model covers the propagation reactor's proposal
// cache (consensus/propagation/commitment_state.go), the compact block, have,
// recovery-part, catch-up and retry paths, and the consensus side that
// consumes proposalChan and partChan (consensus/state.go: receiveRoutine,
// syncData). Read README.md in this directory for the abstractions, the
// properties, the trace schema and the mapping to Go functions and fuzz ops.
//
// Processes (each is one Go goroutine; a step of a process is atomic):
//   CONS   consensus receiveRoutine (consensus/state.go). Holds cs.mtx,
//          cs.stateMtx and cs.rsMtx (lockAll) for the whole of each call into
//          the propagator. It is the only consumer of peerMsgQueue.
//   SYNC   consensus syncData (consensus/state.go). The only consumer of
//          proposalChan and partChan; the producer of peerMsgQueue. Takes
//          cs.rsMtx.RLock before it forwards a part.
//   RECV   the propagation reactor's receive goroutine (p2p BaseReactor
//          ProcessorWithReactor): one goroutine for all peers, so it handles
//          one peer message at a time.
//   DELIV  deliverCachedCompactBlock goroutines (post-ab6a3df), at most one
//          per round.
//   retryWants (ticker goroutine, and the `go retryWants()` calls) never
//   blocks; it is one atomic environment step (envRetry).
//
// A Go blocking channel send is a process parked at a program counter
// ("sendProp", "sendParts", "push") until the channel has room. CONS keeps
// cs.rsMtx while parked. The reactor holds no mutex across a channel send
// (pmtx, mtx and the peer-state mutexes guard only short critical sections),
// so each pmtx critical section is folded into the atomic step that runs it.
module propagation {
  // ---------------------------------------------------------------- params
  // original parts per block; a full entry also has K parity parts, and any
  // K of the 2K parts decode the block.
  const K: int
  // the originals 0..MEMPOOL-1 can be rebuilt from the local mempool.
  const MEMPOOL: int
  // channel capacities (Go: proposalChan 1000, partChan 30000,
  // peerMsgQueue 20000).
  const CAP_PROP: int
  const CAP_PART: int
  const CAP_PMQ: int
  const MAX_ROUND: int
  // blocks the proposer key signs. "A" is the honest proposal. A byzantine
  // proposer also signs "B" (another part set) and "X" (A's part set header
  // with another block hash).
  const BLOCKS: Set[str]
  // the part set headers consensus may report through AddCommitment.
  const COMMITS: Set[str]
  // the consensus reactor may queue its own messages (votes) in peerMsgQueue.
  const VOTES: bool
  // ab6a3df, half (a): per-entry forwarded flag / claimForward.
  const FORWARD_ONCE: bool
  // ab6a3df, half (b): handleCachedCompactBlock hands off to a goroutine.
  const ASYNC_DELIVERY: bool
  // record lastAction and the observation logs. They are history variables:
  // no guard reads them. Turn off for exhaustive checking.
  const TRACE: bool
  // track which parts were requested from which peer (needed for L1). With
  // false, every genuine part is admitted (an over-approximation of
  // admitPart, sound for the safety properties) and reqO/reqP stay empty.
  const TRACK_REQUESTS: bool
  // peers send haves and recovery parts. With false (and MEMPOOL = 0) no
  // part ever arrives: the configuration checks forwarding and identity
  // logic without the part dimension.
  const PARTS: bool
  const LOG_CAP: int

  pure val PEERS = Set("p1", "p2")
  pure val ROUNDS = 0.to(MAX_ROUND)
  pure val ORIG = 0.to(K - 1)
  pure val MEMSET = 0.to(MEMPOOL - 1)
  pure val KINDS = Set("orig", "parity")

  // ----------------------------------------------------------------- types
  // One proposalData entry of ProposalCache.proposals[H][round].
  //   kind "none": no entry.
  //   kind "full": built from a compact block (AddProposal); block is its
  //                identity, psh its part set header; has a parity set.
  //   kind "stub": built by AddCommitment from a part set header only; its
  //                compactBlock has a zero BlockID, so sameProposalIdentity
  //                compares psh only; it has no parity set and cannot decode.
  // orig: held original part indexes (subset of 0..K-1).
  // par:  number of held parity parts (indexes K..K+par-1).
  type Entry = {
    kind: str, block: str, psh: str, forwarded: bool, catchup: bool,
    orig: Set[int], par: int
  }
  // PeerState.unverifiedProposals[H] of one peer.
  type Unv = { has: bool, round: int, block: str }
  // propagation reactor state guarded by pmtx / peer-state mutexes.
  type Prop = {
    round: int,                    // ProposalCache.round (the reactor's round)
    entries: int -> Entry,         // ProposalCache.proposals[H]
    unverified: str -> Unv,        // per peer
    reqO: Set[(str, int)],         // (peer, round): missing originals requested
    reqP: Set[(str, int)],         // (peer, round): parity requested (haves)
    gossiped: Set[(int, str)]      // (round, block) sent by broadcastCompactBlock
  }
  type ProposalMsg = { round: int, block: str, from: str }
  // an item of peerMsgQueue: kind "proposal" | "part" | "vote".
  type Msg = { kind: str, round: int, block: str, from: str }
  type Cons = {
    round: int,      // cs.rs.Round
    pc: str,         // "idle" | "apply" | "sendProp" | "sendParts"
    cbRound: int,    // cached compact block being applied
    cbBlock: str,
    peer: str,       // the peer whose cached compact block is applied
    retried: bool,   // applyCachedProposalIfAvailable moved to a second peer
    n: int,          // part sends left (pc "sendParts")
    committed: int -> str  // part set header AddCommitment reported per round
  }
  type Sync = { pc: str, msg: Msg }   // pc: "idle" | "push" | "needRs"
  type Recv = {
    pc: str,         // "idle" | "sendProp" | "gossip" | "sendParts"
    round: int, block: str, from: str,
    added: bool,     // AddProposal returned true
    n: int,          // partChan sends left
    decodeAfter: bool // after the sends, try to decode (handleRecoveryPart)
  }
  type Deliv = { pc: str, block: str, n: int }  // pc: "none" | "sendProp" | "recover" | "sendParts"
  type Hist = {
    fwd: Set[(int, str)],   // (round, block) sent into proposalChan
    dupForward: bool,       // some (round, block) was sent twice
    s3Decision: bool,       // a forward was decided for a block conflicting with the entry
    s3Gossip: bool,         // a compact block was gossiped while conflicting with the entry
    s3Deliver: bool,        // a proposal entered proposalChan while conflicting with the entry
    delivOverwrite: bool    // a DELIV goroutine was spawned on a busy slot (model sanity)
  }
  type Obs = { propLog: List[ProposalMsg], consLog: List[ProposalMsg] }
  // The step just taken. env = true: an environment action (peer message,
  // consensus call, tick) that a replay harness drives; env = false: an
  // internal scheduling step (channel send/receive, goroutine progress).
  type Action = {
    kind: str, env: bool, peer: str, round: int, block: str, psh: str,
    part: str, idx: int, corrupt: bool, proof: bool, accepted: bool
  }

  // ------------------------------------------------------------- variables
  var prop: Prop
  var cons: Cons
  var syncd: Sync
  var recv: Recv
  var deliv: int -> Deliv
  var propChan: List[ProposalMsg]
  var partChan: List[int]          // round of each queued part
  var pmq: List[Msg]               // cs.peerMsgQueue
  var hist: Hist
  var obs: Obs
  var lastAction: Action

  // --------------------------------------------------------------- helpers
  pure val noEntry: Entry = {
    kind: "none", block: "", psh: "", forwarded: false, catchup: false,
    orig: Set(), par: 0
  }
  pure val noUnv: Unv = { has: false, round: -1, block: "" }
  pure val noMsg: Msg = { kind: "", round: -1, block: "", from: "" }
  pure val recvIdle: Recv = {
    pc: "idle", round: -1, block: "", from: "", added: false, n: 0, decodeAfter: false
  }
  pure val delivNone: Deliv = { pc: "none", block: "", n: 0 }
  pure val noAction: Action = {
    kind: "init", env: false, peer: "", round: -1, block: "", psh: "",
    part: "", idx: -1, corrupt: false, proof: false, accepted: false
  }
  pure def mkAct(kind: str, env: bool): Action = { ...noAction, kind: kind, env: env }

  // X shares A's part set header (same parts) but has another block hash.
  pure def pshOf(b: str): str = if (b == "X") "A" else b

  pure def fullEntry(b: str): Entry = { ...noEntry, kind: "full", block: b, psh: pshOf(b) }

  // sameProposalIdentity (catchup.go)
  pure def sameIdentity(e: Entry, b: str): bool =
    e.kind != "none" and e.psh == pshOf(b) and (e.kind == "stub" or e.block == b)

  pure def complete(e: Entry): bool = e.orig.size() == K

  // buildParity runs once the originals are complete (only full entries have
  // a parity set); folded into the step that completes them.
  pure def withParity(e: Entry): Entry =
    if (e.kind == "full" and complete(e)) { ...e, par: K } else e

  // CombinedPartSet.CanDecode: enough parts and not a catch-up part set.
  pure def canDecode(e: Entry): bool =
    e.kind == "full" and not(complete(e)) and e.orig.size() + e.par >= K

  pure def maxOf(s: Set[int]): int = s.fold(-1, (a, i) => if (i > a) i else a)

  // The index an environment part message carries for a kind. Indexes of one
  // kind are interchangeable, so the environment sends a canonical index:
  // the highest missing original outside the mempool set (if any), else the
  // highest missing original; parity parts in increasing order.
  pure def nextIdx(e: Entry, kind: str): int =
    if (kind == "orig") {
      val miss = ORIG.exclude(e.orig)
      val pref = miss.exclude(MEMSET)
      if (pref.size() > 0) maxOf(pref) else maxOf(miss)
    } else K + e.par

  // recoverPartsFromMempool (commitment.go): rebuilds the mempool originals
  // missing from the entry at (H, round) when that entry has the compact
  // block's part set header; returns the new entry and the number of parts
  // it then sends to partChan (publishLocalParts). getAllState(.., false)
  // requires the round to be relevant.
  pure def memRecover(e: Entry, b: str, relevant: bool): (Entry, int) =
    if (relevant and e.kind != "none" and e.psh == pshOf(b)) {
      val added = MEMSET.exclude(e.orig)
      (withParity({ ...e, orig: e.orig.union(MEMSET) }), added.size())
    } else (e, 0)

  // retryWants (catchup.go): for every entry unfinishedHeights returns that
  // is not complete, request the missing originals from every peer.
  // unfinishedHeights returns the latest round's entry if it is catchup or
  // incomplete, and older rounds' entries that are catchup and incomplete.
  pure def retryReqs(entries: int -> Entry, reqO: Set[(str, int)]): Set[(str, int)] = if (not(TRACK_REQUESTS)) reqO else {
    val present = ROUNDS.filter(r => entries.get(r).kind != "none")
    val latest = maxOf(present)
    val elig = present.filter(r => not(complete(entries.get(r))) and (r == latest or entries.get(r).catchup))
    reqO.union(tuples(PEERS, elig))
  }

  // relevant (commitment_state.go) at the fixed height.
  pure def relevantAt(p: Prop, r: int): bool = r >= p.round

  pure def recordForward(h: Hist, r: int, b: str, entries: int -> Entry): Hist = {
    ...h,
    dupForward: h.dupForward or h.fwd.contains((r, b)),
    fwd: h.fwd.union(Set((r, b))),
    s3Deliver: h.s3Deliver or not(sameIdentity(entries.get(r), b))
  }

  // Channel contents that no guard reads (proposalChan and peerMsgQueue
  // items; only their number matters) are kept only under TRACE.
  pure val noProp: ProposalMsg = { round: -1, block: "", from: "" }
  pure def chanItem(m: ProposalMsg): ProposalMsg = if (TRACE) m else noProp

  pure def capAppend(l: List[ProposalMsg], m: ProposalMsg): List[ProposalMsg] =
    if (l.length() < LOG_CAP) l.append(m) else l

  // history updates, only with TRACE
  def traced(a: Action): Action = if (TRACE) a else lastAction
  def logProp(m: ProposalMsg): Obs = if (TRACE) { ...obs, propLog: capAppend(obs.propLog, m) } else obs
  def logCons(m: ProposalMsg): Obs = if (TRACE) { ...obs, consLog: capAppend(obs.consLog, m) } else obs

  // --------------------------------------------------------- init
  action init = all {
    // newReactorFuzzer: reactor started, SetConsensusState(H, 0, proposer).
    prop' = {
      round: 0,
      entries: ROUNDS.mapBy(_ => noEntry),
      unverified: PEERS.mapBy(_ => noUnv),
      reqO: Set(), reqP: Set(), gossiped: Set()
    },
    cons' = {
      round: 0, pc: "idle", cbRound: -1, cbBlock: "", peer: "", retried: false, n: 0,
      committed: ROUNDS.mapBy(_ => "")
    },
    syncd' = { pc: "idle", msg: noMsg },
    recv' = recvIdle,
    deliv' = ROUNDS.mapBy(_ => delivNone),
    propChan' = List(), partChan' = List(), pmq' = List(),
    hist' = {
      fwd: Set(), dupForward: false, s3Decision: false, s3Gossip: false,
      s3Deliver: false, delivOverwrite: false
    },
    obs' = { propLog: List(), consLog: List() },
    lastAction' = noAction,
  }

  // =============================================== RECV: peer messages (env)

  // ReceiveEnvelope -> handleCompactBlock -> validateCompactBlock, then
  // processValidatedCompactBlock up to the proposalChan send: AddProposal,
  // conflictsWith, claimForward (three pmtx sections; claimForward re-checks
  // the identity, so merging them hides no unsafe interleaving).
  // Fuzz op 0 (b = "A") or op 1 (re-signed byzantine block).
  action envCompactBlock(peer: str, r: int, b: str): bool = {
    val e = prop.entries.get(r)
    // the proposer key is installed and only proposer-signed compact blocks
    // exist (a peer cannot forge), so validation fails only on the round.
    val valid = r == prop.round
    val added = e.kind == "none"
    val e1 = if (added) fullEntry(b) else e
    val conflict = not(added) and not(sameIdentity(e, b))
    val fwd = if (FORWARD_ONCE) not(e1.forwarded) and sameIdentity(e1, b) else true
    val e2 = if (FORWARD_ONCE and fwd) { ...e1, forwarded: true } else e1
    val a = { ...mkAct("compactBlock", true), peer: peer, round: r, block: b, psh: pshOf(b), accepted: valid and not(conflict) }
    all {
      recv.pc == "idle",
      BLOCKS.contains(b),
      if (not(valid)) all {
        // validation failed: StoreUnverifiedProposal (last write wins)
        prop' = { ...prop, unverified: prop.unverified.set(peer, { has: true, round: r, block: b }) },
        recv' = recv,
        hist' = hist,
      } else if (conflict) all {
        prop' = prop, recv' = recv, hist' = hist,
      } else all {
        prop' = { ...prop, entries: prop.entries.set(r, e2) },
        recv' =
          if (fwd) { ...recvIdle, pc: "sendProp", round: r, block: b, from: if (TRACE) peer else "", added: added }
          else if (added) { ...recvIdle, pc: "gossip", round: r, block: b, from: if (TRACE) peer else "", added: true }
          else recvIdle,
        hist' = if (fwd) { ...hist, s3Decision: hist.s3Decision or not(sameIdentity(e1, b)) } else hist,
      },
      cons' = cons, syncd' = syncd, deliv' = deliv,
      propChan' = propChan, partChan' = partChan, pmq' = pmq, obs' = obs,
      lastAction' = traced(a),
    }
  }

  // handleHaves (have_wants.go) plus the requestFromPeer goroutine that turns
  // the have into a want (folded: it only does TrySend). A have for a part we
  // lack, of a full entry at a relevant round, makes us request it from the
  // sender. Haves with bad hashes are dropped (no state change; not modelled).
  // Fuzz op 3.
  action envHave(peer: str, r: int, kind: str): bool = {
    val e = prop.entries.get(r)
    val missing = if (kind == "orig") not(complete(e)) else e.par < K
    val ok = e.kind == "full" and relevantAt(prop, r) and not(complete(e)) and missing
    val a = { ...mkAct("have", true), peer: peer, round: r, part: kind, idx: nextIdx(e, kind), accepted: ok }
    all {
      recv.pc == "idle",
      prop' =
        if (not(ok) or not(TRACK_REQUESTS)) prop
        else if (kind == "orig") { ...prop, reqO: prop.reqO.union(Set((peer, r))) }
        else { ...prop, reqP: prop.reqP.union(Set((peer, r))) },
      cons' = cons, syncd' = syncd, recv' = recv, deliv' = deliv,
      propChan' = propChan, partChan' = partChan, pmq' = pmq, hist' = hist, obs' = obs,
      lastAction' = traced(a),
    }
  }

  // handleWants (have_wants.go) only TrySends parts we hold and parks wants
  // in peer state; it never blocks and never touches the proposal cache or
  // forwarding, so it leaves the modelled state unchanged. Fuzz op 4.
  action envWant(peer: str, r: int): bool = all {
    recv.pc == "idle",
    prop' = prop, cons' = cons, syncd' = syncd, recv' = recv, deliv' = deliv,
    propChan' = propChan, partChan' = partChan, pmq' = pmq, hist' = hist, obs' = obs,
    lastAction' = traced({ ...mkAct("want", true), peer: peer, round: r }),
  }

  // whether handleRecoveryPart adds the part (see envPart)
  def partAccepted(peer: str, r: int, kind: str, corrupt: bool, proof: bool): bool = {
    val e = prop.entries.get(r)
    // getCachedState(H, r, proof): an entry must exist; without a proof the
    // round must be relevant.
    val known = e.kind != "none" and (proof or relevantAt(prop, r))
    val missing = if (kind == "orig") not(complete(e)) else e.par < K
    // catch-up entries hold no parity set to add to
    val parityOK = kind == "orig" or e.kind == "full"
    // admitPart: requested (push allowance not modelled)
    val admitted = not(TRACK_REQUESTS) or (if (kind == "orig") prop.reqO.contains((peer, r)) else prop.reqP.contains((peer, r)))
    // a stub has no part hashes: the part must bring its proof
    val proofOK = e.kind == "full" or proof
    known and missing and parityOK and admitted and not(corrupt) and proofOK
  }

  // handleRecoveryPart (have_wants.go). corrupt: data that does not match
  // the part set (fuzz op 5); otherwise a genuine part (fuzz op 6). proof:
  // the part carries a Merkle proof (answers to catch-up wants do).
  action envPart(peer: str, r: int, kind: str, corrupt: bool, proof: bool): bool = {
    val e = prop.entries.get(r)
    val idx = nextIdx(e, kind)
    val accept = partAccepted(peer, r, kind, corrupt, proof)
    val eOrig = withParity({ ...e, orig: e.orig.union(Set(idx)) })
    val ePar = { ...e, par: e.par + 1 }
    val decodes = canDecode(ePar)
    val eDec = { ...e, orig: ORIG, par: K }
    val a = {
      ...mkAct("part", true), peer: peer, round: r, part: kind, idx: idx,
      corrupt: corrupt, proof: proof, accepted: accept
    }
    all {
      recv.pc == "idle",
      if (not(accept)) all {
        prop' = prop, recv' = recv,
      } else if (kind == "orig") all {
        // AddPart, then the blocking partChan send, then (after it) decoding
        prop' = { ...prop, entries: prop.entries.set(r, eOrig) },
        recv' = { ...recvIdle, pc: "sendParts", round: r, n: 1, decodeAfter: true },
      } else if (decodes) all {
        // parity is not sent to consensus; RecoverOriginals rebuilds the
        // missing originals, publishLocalParts sends them (blocking)
        prop' = { ...prop, entries: prop.entries.set(r, eDec) },
        recv' = { ...recvIdle, pc: "sendParts", round: r, n: K - e.orig.size(), decodeAfter: false },
      } else all {
        prop' = { ...prop, entries: prop.entries.set(r, ePar) },
        recv' = recv,
      },
      cons' = cons, syncd' = syncd, deliv' = deliv,
      propChan' = propChan, partChan' = partChan, pmq' = pmq, hist' = hist, obs' = obs,
      lastAction' = traced(a),
    }
  }

  // ======================================== RECV: internal continuation steps

  // processValidatedCompactBlock: the blocking proposalChan send.
  action recvSendProp: bool = {
    val m = { round: recv.round, block: recv.block, from: recv.from }
    all {
      recv.pc == "sendProp",
      propChan.length() < CAP_PROP,
      propChan' = propChan.append(chanItem(m)),
      hist' = recordForward(hist, recv.round, recv.block, prop.entries),
      recv' = if (recv.added) { ...recv, pc: "gossip" } else recvIdle,
      obs' = logProp(m),
      prop' = prop, cons' = cons, syncd' = syncd, deliv' = deliv,
      partChan' = partChan, pmq' = pmq,
      lastAction' = traced({ ...mkAct("recvSendProp", false), peer: recv.from, round: recv.round, block: recv.block }),
    }
  }

  // processValidatedCompactBlock after the send, when AddProposal added the
  // entry: broadcastCompactBlock (no identity re-check), then
  // recoverPartsFromMempool.
  action recvGossip: bool = {
    val r = recv.round
    val e = prop.entries.get(r)
    val rec = memRecover(e, recv.block, relevantAt(prop, r))
    all {
      recv.pc == "gossip",
      // gossiped is observation only (no guard reads it): kept under TRACE
      prop' = {
        ...prop,
        entries: prop.entries.set(r, rec._1),
        gossiped: if (TRACE) prop.gossiped.union(Set((r, recv.block))) else prop.gossiped
      },
      hist' = { ...hist, s3Gossip: hist.s3Gossip or not(sameIdentity(e, recv.block)) },
      recv' = if (rec._2 > 0) { ...recvIdle, pc: "sendParts", round: r, n: rec._2, decodeAfter: false } else recvIdle,
      cons' = cons, syncd' = syncd, deliv' = deliv,
      propChan' = propChan, partChan' = partChan, pmq' = pmq, obs' = obs,
      lastAction' = traced({ ...mkAct("recvGossip", false), peer: recv.from, round: r, block: recv.block, idx: rec._2 }),
    }
  }

  // one blocking partChan send (handleRecoveryPart / publishLocalParts).
  // After the last send of an accepted part, handleRecoveryPart tries to
  // decode on the current entry.
  action recvSendPart: bool = {
    val r = recv.round
    val e = prop.entries.get(r)
    val last = recv.n == 1
    val dec = last and recv.decodeAfter and canDecode(e)
    all {
      recv.pc == "sendParts",
      partChan.length() < CAP_PART,
      partChan' = partChan.append(r),
      prop' = if (dec) { ...prop, entries: prop.entries.set(r, { ...e, orig: ORIG, par: K }) } else prop,
      recv' =
        if (not(last)) { ...recv, n: recv.n - 1 }
        else if (dec) { ...recvIdle, pc: "sendParts", round: r, n: K - e.orig.size(), decodeAfter: false }
        else recvIdle,
      cons' = cons, syncd' = syncd, deliv' = deliv,
      propChan' = propChan, pmq' = pmq, hist' = hist, obs' = obs,
      lastAction' = traced({ ...mkAct("recvSendPart", false), round: r, accepted: dec }),
    }
  }

  // ==================================== consensus calls into the propagator

  // Consensus enters round r (enterNewRound, under lockAll) and calls
  // SetConsensusState(H, r, proposer): installs the round, then
  // applyCachedProposalIfAvailable: if no entry exists at (H, r), pick a peer
  // whose cached compact block is for round r (it validates: right round,
  // proposer-signed). Peer order is Go map order, so the choice is free.
  // The pick is applied in the next CONS step (consApply); RECV may run in
  // between, as it can in Go between GetProposal and AddProposal.
  // Fuzz op 8 (same or next round, correct proposer).
  action envEnterRound(r: int, c: str): bool = {
    val cands = PEERS.filter(p => prop.unverified.get(p).has and prop.unverified.get(p).round == r)
    val apply = prop.entries.get(r).kind == "none" and cands.size() > 0
    all {
      cons.pc == "idle",
      r == cons.round or r == cons.round + 1,
      ROUNDS.contains(r),
      // c is the chosen peer when one is applied, and is fixed otherwise
      if (apply) cands.contains(c) else c == "p1",
      prop' = { ...prop, round: r },
      cons' =
        if (apply) { ...cons, round: r, pc: "apply", cbRound: r, cbBlock: prop.unverified.get(c).block, peer: c, retried: false }
        else { ...cons, round: r },
      syncd' = syncd, recv' = recv, deliv' = deliv,
      propChan' = propChan, partChan' = partChan, pmq' = pmq, hist' = hist, obs' = obs,
      lastAction' = traced({ ...mkAct("enterRound", true), round: r, peer: if (apply) c else "" }),
    }
  }

  // handleCachedCompactBlock (catchup.go) for the picked cached block, still
  // on the consensus receive routine with its locks held.
  action consApply: bool = {
    val r = cons.cbRound
    val b = cons.cbBlock
    val e = prop.entries.get(r)
    val added = relevantAt(prop, r) and e.kind == "none"
    val e1 = if (added) fullEntry(b) else e
    val same = sameIdentity(e1, b)
    val fwd = if (FORWARD_ONCE) not(e1.forwarded) else true
    val e2 = { ...e1, catchup: true, forwarded: if (FORWARD_ONCE) true else e1.forwarded }
    // on a conflict applyCachedProposalIfAvailable tries the next peer
    val others = PEERS.filter(p => p != cons.peer and prop.unverified.get(p).has and prop.unverified.get(p).round == r)
    // with two peers there is at most one other peer to try
    val nxt = if (others.contains("p1")) "p1" else "p2"
    val rec = memRecover(e2, b, relevantAt(prop, r))
    all {
      cons.pc == "apply",
      if (not(same)) all {
        // conflict: handleCachedCompactBlock returns false and the cached
        // block stays; the loop goes on to the next peer's cached block
        prop' = { ...prop, entries: prop.entries.set(r, e1) },
        cons' =
          if (not(cons.retried) and others.size() > 0)
            { ...cons, peer: nxt, cbBlock: prop.unverified.get(nxt).block, retried: true }
          else { ...cons, pc: "idle", cbRound: -1, cbBlock: "", peer: "", retried: false },
        deliv' = deliv, hist' = hist,
      } else if (ASYNC_DELIVERY) all {
        // post-ab6a3df: go deliverCachedCompactBlock(cb, forward)
        prop' = { ...prop, entries: prop.entries.set(r, e2), unverified: prop.unverified.set(cons.peer, noUnv) },
        cons' = { ...cons, pc: "idle", cbRound: -1, cbBlock: "", peer: "", retried: false },
        deliv' = deliv.set(r, { pc: if (fwd) "sendProp" else "recover", block: b, n: 0 }),
        hist' = {
          ...hist,
          s3Decision: hist.s3Decision or (fwd and not(same)),
          delivOverwrite: hist.delivOverwrite or deliv.get(r).pc != "none"
        },
      } else if (fwd) all {
        // pre-ab6a3df: blocking proposalChan send on the receive routine.
        // DeleteUnverifiedProposal runs only after handleCachedCompactBlock
        // returns.
        prop' = { ...prop, entries: prop.entries.set(r, e2) },
        cons' = { ...cons, pc: "sendProp" },
        deliv' = deliv, hist' = hist,
      } else all {
        // forward-once without async delivery: no proposal send, but the
        // mempool parts are still sent from the receive routine
        prop' = {
          ...prop,
          entries: prop.entries.set(r, rec._1),
          unverified: prop.unverified.set(cons.peer, noUnv),
          reqO: if (rec._2 > 0) prop.reqO else retryReqs(prop.entries.set(r, rec._1), prop.reqO)
        },
        cons' = if (rec._2 > 0) { ...cons, pc: "sendParts", n: rec._2 } else { ...cons, pc: "idle", cbRound: -1, cbBlock: "", peer: "", retried: false },
        deliv' = deliv, hist' = hist,
      },
      syncd' = syncd, recv' = recv,
      propChan' = propChan, partChan' = partChan, pmq' = pmq, obs' = obs,
      lastAction' = traced({ ...mkAct("consApply", false), peer: cons.peer, round: r, block: b, accepted: same }),
    }
  }

  // pre-ab6a3df handleCachedCompactBlock: the blocking proposalChan send on
  // the receive routine, then recoverPartsFromMempool, then go retryWants().
  action consSendProp: bool = {
    val r = cons.cbRound
    val m = { round: r, block: cons.cbBlock, from: "self" }
    val rec = memRecover(prop.entries.get(r), cons.cbBlock, relevantAt(prop, r))
    val ents = prop.entries.set(r, rec._1)
    all {
      cons.pc == "sendProp",
      propChan.length() < CAP_PROP,
      propChan' = propChan.append(chanItem(m)),
      hist' = recordForward(hist, r, cons.cbBlock, prop.entries),
      prop' = {
        ...prop,
        entries: ents,
        unverified: prop.unverified.set(cons.peer, noUnv),
        reqO: if (rec._2 > 0) prop.reqO else retryReqs(ents, prop.reqO)
      },
      cons' = if (rec._2 > 0) { ...cons, pc: "sendParts", n: rec._2 } else { ...cons, pc: "idle", cbRound: -1, cbBlock: "", peer: "", retried: false },
      obs' = logProp(m),
      syncd' = syncd, recv' = recv, deliv' = deliv, partChan' = partChan, pmq' = pmq,
      lastAction' = traced({ ...mkAct("consSendProp", false), round: r, block: cons.cbBlock }),
    }
  }

  // pre-ab6a3df: publishLocalParts' blocking partChan sends on the receive
  // routine; the last one is followed by go retryWants() (folded).
  action consSendPart: bool = all {
    cons.pc == "sendParts",
    partChan.length() < CAP_PART,
    partChan' = partChan.append(cons.cbRound),
    cons' = if (cons.n > 1) { ...cons, n: cons.n - 1 } else { ...cons, pc: "idle", n: 0, cbRound: -1, cbBlock: "", peer: "", retried: false },
    prop' = if (cons.n > 1) prop else { ...prop, reqO: retryReqs(prop.entries, prop.reqO) },
    syncd' = syncd, recv' = recv, deliv' = deliv, propChan' = propChan, pmq' = pmq, hist' = hist, obs' = obs,
    lastAction' = traced({ ...mkAct("consSendPart", false), round: cons.cbRound }),
  }

  // AddCommitment(H, r, psh) (catchup.go), from consensus under lockAll.
  // Never blocks; its `go retryWants()` is left to envRetry. COMMITS bounds
  // the headers consensus may report, and one round gets one header (two
  // would need two quorums in one round).
  // Fuzz op 10.
  action envAddCommitment(r: int, psh: str): bool = {
    val e = prop.entries.get(r)
    val same = e.kind != "none" and e.psh == psh
    val stub = { ...noEntry, kind: "stub", psh: psh, catchup: true }
    all {
      cons.pc == "idle",
      COMMITS.contains(psh),
      cons.committed.get(r) == "" or cons.committed.get(r) == psh,
      if (same) all {
        prop' = { ...prop, entries: prop.entries.set(r, if (not(e.catchup) and not(complete(e))) { ...e, catchup: true } else e) },
      } else all {
        // replace: fresh part set, per-peer state of the round wiped
        // (DeleteRound), the reactor's round reset to 0
        prop' = {
          ...prop,
          round: 0,
          entries: prop.entries.set(r, stub),
          reqO: prop.reqO.filter(x => x._2 != r),
          reqP: prop.reqP.filter(x => x._2 != r)
        },
      },
      cons' = { ...cons, committed: cons.committed.set(r, psh) },
      syncd' = syncd, recv' = recv, deliv' = deliv,
      propChan' = propChan, partChan' = partChan, pmq' = pmq, hist' = hist, obs' = obs,
      lastAction' = traced({ ...mkAct("addCommitment", true), round: r, psh: psh, accepted: not(same) }),
    }
  }

  // Prune(H) (reactor.go) after consensus commits H: peers drop unverified
  // proposals at heights <= H; proposals below H are deleted (none here);
  // serveCommitted runs in a goroutine and finds nothing (H is not in the
  // block store in this model). Never blocks. Direct call r.Prune(5).
  action envPrune: bool = all {
    cons.pc == "idle",
    prop' = { ...prop, unverified: PEERS.mapBy(_ => noUnv) },
    cons' = cons, syncd' = syncd, recv' = recv, deliv' = deliv,
    propChan' = propChan, partChan' = partChan, pmq' = pmq, hist' = hist, obs' = obs,
    lastAction' = traced(mkAct("prune", true)),
  }

  // retryWants (catchup.go), from the ticker goroutine or a `go
  // retryWants()`. Only TrySend; atomic. Fuzz op 11.
  action envRetry: bool = all {
    prop' = { ...prop, reqO: retryReqs(prop.entries, prop.reqO) },
    cons' = cons, syncd' = syncd, recv' = recv, deliv' = deliv,
    propChan' = propChan, partChan' = partChan, pmq' = pmq, hist' = hist, obs' = obs,
    lastAction' = traced(mkAct("retryWants", true)),
  }

  // the consensus reactor (votes etc.) queues a message in peerMsgQueue.
  // Its own blocking on a full queue is not modelled (it holds no lock).
  action envVote: bool = all {
    VOTES,
    pmq.length() < CAP_PMQ,
    pmq' = pmq.append(if (TRACE) { ...noMsg, kind: "vote" } else noMsg),
    prop' = prop, cons' = cons, syncd' = syncd, recv' = recv, deliv' = deliv,
    propChan' = propChan, partChan' = partChan, hist' = hist, obs' = obs,
    lastAction' = traced(mkAct("vote", true)),
  }

  // ================================================ CONS: receive routine

  // receiveRoutine takes a peerMsgQueue item and handles it under lockAll
  // (handleMsg); a ProposalMessage is the proposal reaching consensus.
  action consHandle: bool = {
    val m = pmq.head()
    all {
      cons.pc == "idle",
      pmq.length() > 0,
      pmq' = pmq.tail(),
      obs' = if (m.kind == "proposal") logCons({ round: m.round, block: m.block, from: m.from }) else obs,
      prop' = prop, cons' = cons, syncd' = syncd, recv' = recv, deliv' = deliv,
      propChan' = propChan, partChan' = partChan, hist' = hist,
      lastAction' = traced({ ...mkAct("consHandle", false), part: m.kind, round: m.round, block: m.block }),
    }
  }

  // ======================================================= SYNC: syncData

  action syncTakeProp: bool = {
    val m = propChan.head()
    all {
      syncd.pc == "idle",
      propChan.length() > 0,
      propChan' = propChan.tail(),
      syncd' = { pc: "push", msg: { kind: "proposal", round: m.round, block: m.block, from: m.from } },
      prop' = prop, cons' = cons, recv' = recv, deliv' = deliv,
      partChan' = partChan, pmq' = pmq, hist' = hist, obs' = obs,
      lastAction' = traced({ ...mkAct("syncTakeProp", false), round: m.round, block: m.block }),
    }
  }

  action syncTakePart: bool = {
    val r = partChan.head()
    all {
      syncd.pc == "idle",
      partChan.length() > 0,
      partChan' = partChan.tail(),
      syncd' = { pc: "needRs", msg: { ...noMsg, kind: "part", round: r } },
      prop' = prop, cons' = cons, recv' = recv, deliv' = deliv,
      propChan' = propChan, pmq' = pmq, hist' = hist, obs' = obs,
      lastAction' = traced({ ...mkAct("syncTakePart", false), round: r }),
    }
  }

  // cs.rsMtx.RLock(): blocks while CONS is inside a call (lockAll held).
  // Keeps parts of the current round (forCommit/forLock and the
  // ProposalBlockParts filters are not modelled: they only drop more).
  action syncReadRs: bool = all {
    syncd.pc == "needRs",
    cons.pc == "idle",
    syncd' = if (syncd.msg.round == cons.round) { pc: "push", msg: if (TRACE) syncd.msg else noMsg } else { pc: "idle", msg: noMsg },
    prop' = prop, cons' = cons, recv' = recv, deliv' = deliv,
    propChan' = propChan, partChan' = partChan, pmq' = pmq, hist' = hist, obs' = obs,
    lastAction' = traced({ ...mkAct("syncReadRs", false), round: syncd.msg.round, accepted: syncd.msg.round == cons.round }),
  }

  // blocking peerMsgQueue send.
  action syncPush: bool = all {
    syncd.pc == "push",
    pmq.length() < CAP_PMQ,
    pmq' = pmq.append(if (TRACE) syncd.msg else noMsg),
    syncd' = { pc: "idle", msg: noMsg },
    prop' = prop, cons' = cons, recv' = recv, deliv' = deliv,
    propChan' = propChan, partChan' = partChan, hist' = hist, obs' = obs,
    lastAction' = traced({ ...mkAct("syncPush", false), part: syncd.msg.kind, round: syncd.msg.round, block: syncd.msg.block }),
  }

  // ============================ DELIV: deliverCachedCompactBlock goroutines

  action delivSendProp(r: int): bool = {
    val d = deliv.get(r)
    val m = { round: r, block: d.block, from: "self" }
    all {
      d.pc == "sendProp",
      propChan.length() < CAP_PROP,
      propChan' = propChan.append(chanItem(m)),
      hist' = recordForward(hist, r, d.block, prop.entries),
      deliv' = deliv.set(r, { ...d, pc: "recover" }),
      obs' = logProp(m),
      prop' = prop, cons' = cons, syncd' = syncd, recv' = recv, partChan' = partChan, pmq' = pmq,
      lastAction' = traced({ ...mkAct("delivSendProp", false), round: r, block: d.block }),
    }
  }

  // recoverPartsFromMempool; when nothing is sent, the closing retryWants.
  action delivRecover(r: int): bool = {
    val d = deliv.get(r)
    val rec = memRecover(prop.entries.get(r), d.block, relevantAt(prop, r))
    val ents = prop.entries.set(r, rec._1)
    all {
      d.pc == "recover",
      prop' = { ...prop, entries: ents, reqO: if (rec._2 > 0) prop.reqO else retryReqs(ents, prop.reqO) },
      deliv' = deliv.set(r, if (rec._2 > 0) { ...d, pc: "sendParts", n: rec._2 } else delivNone),
      cons' = cons, syncd' = syncd, recv' = recv,
      propChan' = propChan, partChan' = partChan, pmq' = pmq, hist' = hist, obs' = obs,
      lastAction' = traced({ ...mkAct("delivRecover", false), round: r, block: d.block, idx: rec._2 }),
    }
  }

  // publishLocalParts' blocking sends; the last is followed by retryWants.
  action delivSendPart(r: int): bool = {
    val d = deliv.get(r)
    all {
      d.pc == "sendParts",
      partChan.length() < CAP_PART,
      partChan' = partChan.append(r),
      deliv' = deliv.set(r, if (d.n > 1) { ...d, n: d.n - 1 } else delivNone),
      prop' = if (d.n > 1) prop else { ...prop, reqO: retryReqs(prop.entries, prop.reqO) },
      cons' = cons, syncd' = syncd, recv' = recv,
      propChan' = propChan, pmq' = pmq, hist' = hist, obs' = obs,
      lastAction' = traced({ ...mkAct("delivSendPart", false), round: r }),
    }
  }

  // ================================================================= steps

  action stepCompactBlock = {
    nondet peer = PEERS.oneOf()
    nondet r = ROUNDS.oneOf()
    nondet b = BLOCKS.oneOf()
    envCompactBlock(peer, r, b)
  }
  action stepHave = {
    nondet peer = PEERS.oneOf()
    nondet r = ROUNDS.oneOf()
    nondet kind = KINDS.oneOf()
    all { PARTS, envHave(peer, r, kind) }
  }
  action stepWant = {
    nondet peer = PEERS.oneOf()
    nondet r = ROUNDS.oneOf()
    envWant(peer, r)
  }
  action stepPart = {
    nondet peer = PEERS.oneOf()
    nondet r = ROUNDS.oneOf()
    nondet kind = KINDS.oneOf()
    nondet corrupt = Bool.oneOf()
    nondet proof = Bool.oneOf()
    all { PARTS, envPart(peer, r, kind, corrupt, proof) }
  }
  action stepEnterRound = {
    nondet r = ROUNDS.oneOf()
    nondet c = PEERS.oneOf()
    envEnterRound(r, c)
  }
  action stepAddCommitment = {
    nondet r = ROUNDS.oneOf()
    nondet psh = COMMITS.oneOf()
    envAddCommitment(r, psh)
  }
  action stepDeliv = {
    nondet r = ROUNDS.oneOf()
    any { delivSendProp(r), delivRecover(r), delivSendPart(r) }
  }

  action envStep = any {
    stepCompactBlock, stepHave, stepWant, stepPart,
    stepEnterRound, stepAddCommitment, envPrune, envRetry, envVote,
  }

  action internalStep = any {
    recvSendProp, recvGossip, recvSendPart,
    consApply, consSendProp, consSendPart, consHandle,
    syncTakeProp, syncTakePart, syncReadRs, syncPush,
    stepDeliv,
  }

  action step = any { envStep, internalStep }

  // ======================================================= wait-for graph

  // A process is blocked when it is parked on a channel send with the
  // channel full, or on a mutex another process holds. waitsOn names the
  // processes that would have to act to unblock it (the channel's only
  // consumer, or the mutex holder).
  val PROCS = Set("cons", "sync", "recv", "deliv0", "deliv1")
  val consHoldsRs = cons.pc != "idle"
  def delivOf(p: str): Deliv = deliv.get(if (p == "deliv0") 0 else 1)
  def blocked(p: str): bool =
    if (p == "cons")
      (cons.pc == "sendProp" and propChan.length() >= CAP_PROP)
      or (cons.pc == "sendParts" and partChan.length() >= CAP_PART)
    else if (p == "sync")
      (syncd.pc == "push" and pmq.length() >= CAP_PMQ) or (syncd.pc == "needRs" and consHoldsRs)
    else if (p == "recv")
      (recv.pc == "sendProp" and propChan.length() >= CAP_PROP)
      or (recv.pc == "sendParts" and partChan.length() >= CAP_PART)
    else
      ROUNDS.contains(if (p == "deliv0") 0 else 1) and
      ((delivOf(p).pc == "sendProp" and propChan.length() >= CAP_PROP)
      or (delivOf(p).pc == "sendParts" and partChan.length() >= CAP_PART))
  def waitsOn(p: str): Set[str] =
    if (p == "sync") Set("cons") else if (p == "cons") Set("sync") else Set("sync")
  // greatest fixpoint: blocked processes all of whose waitees are in the set
  def shrink(d: Set[str]): Set[str] = d.filter(p => waitsOn(p).subseteq(d))
  val deadlocked: Set[str] = {
    val d0 = PROCS.filter(p => blocked(p))
    shrink(shrink(shrink(shrink(shrink(d0)))))
  }

  // =========================================================== properties

  // S1: no set of processes waits on itself forever.
  val S1_noWaitCycle: bool = deadlocked == Set()
  // the PROTOCO-3430 shape: the cycle closes through peerMsgQueue (syncData
  // parked on a full peerMsgQueue). S1_noWaitCycle also covers cycles that
  // close through cs.rsMtx (syncData parked in needRs).
  val S1_noQueueCycle: bool = not(deadlocked.contains("sync") and syncd.pc == "push")
  // S2: each (H, round) proposal (identity) enters proposalChan at most once.
  val S2_forwardOnce: bool = not(hist.dupForward)
  // S3: a forward to consensus is never decided for a compact block that
  // conflicts with the identity stored for its (H, round) at decision time.
  val S3_decision: bool = not(hist.s3Decision)
  // informational: at most one proposal (any identity) per round
  val S2_strictPerRound: bool = ROUNDS.forall(r => hist.fwd.filter(x => x._1 == r).size() <= 1)
  // model sanity
  val sanity: bool = and {
    propChan.length() <= CAP_PROP, partChan.length() <= CAP_PART, pmq.length() <= CAP_PMQ,
    not(hist.delivOverwrite),
    ROUNDS.forall(r => prop.entries.get(r).orig.subseteq(ORIG) and prop.entries.get(r).par <= K),
  }
  val safety: bool = S1_noWaitCycle and S2_forwardOnce and S3_decision

  // L1 goal: the node holds all K originals of some round's entry.
  val holdsBlock: bool = ROUNDS.exists(r => prop.entries.get(r).kind != "none" and complete(prop.entries.get(r)))

  // ------------------------------------------------- liveness (TLC only)
  // L1 runs with step = liveStep: the honest environment. Peer p1 relays the
  // proposer's compact block and answers what the node requests of it
  // (originals with a proof, as catch-up wants ask; parity without one, as
  // have-driven wants ask); p1 may also send haves. p2 is silent (see
  // README). Consensus may enter rounds, report commitments and prune at
  // any time; the retry ticker runs.
  action p1CB(r: int): bool = envCompactBlock("p1", r, "A")
  action p1Orig(r: int): bool = envPart("p1", r, "orig", false, true)
  action p1Parity(r: int): bool = envPart("p1", r, "parity", false, false)

  action liveEnvStep = {
    nondet r = ROUNDS.oneOf()
    nondet kind = KINDS.oneOf()
    nondet c = PEERS.oneOf()
    nondet psh = COMMITS.oneOf()
    any {
      p1CB(r), p1Orig(r), p1Parity(r), envHave("p1", r, kind),
      envEnterRound(r, c), envAddCommitment(r, psh), envPrune, envRetry,
    }
  }
  action liveStep = any { liveEnvStep, internalStep }

  // a p1 message that makes progress: a compact block that creates the
  // entry of the reactor's round, or a part the node adds.
  action p1Useful = {
    nondet r = ROUNDS.oneOf()
    any {
      all { r == prop.round, prop.entries.get(r).kind == "none", p1CB(r) },
      all { partAccepted("p1", r, "orig", false, true), p1Orig(r) },
      all { partAccepted("p1", r, "parity", false, false), p1Parity(r) },
    }
  }

  val allVars = (prop, cons, syncd, recv, deliv, propChan, partChan, pmq, hist, obs, lastAction)

  // Fairness.
  //  - weak fairness on internalStep: goroutines keep running. Without new
  //    input every internal run is finite (each step consumes a queued item
  //    or advances a pc, and forward-once bounds the proposals), so weak
  //    fairness on the disjunction lets each goroutine finish its work.
  //  - strong fairness on p1Useful: the p2p incoming queue is FIFO, so a
  //    message p1 can send is processed once RECV is idle, which happens
  //    infinitely often.
  //  - weak fairness on envRetry: the retry ticker fires every RetryTime.
  // Consensus calls get no fairness.
  temporal fairness = and {
    weakFair(internalStep, allVars),
    strongFair(p1Useful, allVars),
    weakFair(envRetry, allVars),
  }

  // L1: with an honest proposer (BLOCKS = {A}), the honest peer p1 and the
  // fairness above, the node eventually holds a whole block.
  temporal L1_eventualCompletion = fairness implies eventually(holdsBlock)
  // vacuity check: without fairness L1 must fail (the gate target checks it)
  temporal L1_withoutFairness = eventually(holdsBlock)
}
