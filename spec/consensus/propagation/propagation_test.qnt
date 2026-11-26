// -*- mode: Bluespec; -*-
//
// Scenario tests for propagation.qnt (quint test). The prefix* modules
// replay the two PROTOCO-3430 counterexamples with ab6a3df reverted; the
// fixed* module replays the same inputs with the fix applied.

// ab6a3df reverted (FORWARD_ONCE = ASYNC_DELIVERY = false)
module prefixTest {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = false, ASYNC_DELIVERY = false, TRACE = true, TRACK_REQUESTS = true, PARTS = true, LOG_CAP = 6
  ).* from "./propagation"

  // p1 relays A for round 0; it reaches proposalChan and the node rebuilds
  // the mempool part, which reaches partChan.
  run firstCompactBlock =
    init
      .then(envCompactBlock("p1", 0, "A"))
      .then(recvSendProp)
      .then(recvGossip)
      .then(recvSendPart)
      .expect(propChan.length() == 1 and partChan.length() == 1 and hist.fwd == Set((0, "A")))

  // S2 counterexample: a duplicate of A from p2 is forwarded again.
  run duplicateForwardTest =
    firstCompactBlock
      .then(syncTakeProp)
      .then(envCompactBlock("p2", 0, "A"))
      .then(recvSendProp)
      .expect(not(S2_forwardOnce))

  // S1 counterexample (PROTOCO-3430): p2's compact block for round 1 is
  // cached while the node is in round 0; peerMsgQueue holds a vote; a
  // duplicate refills proposalChan; consensus enters round 1 and, holding
  // its locks, blocks sending the cached proposal to the full proposalChan,
  // while syncData blocks on the full peerMsgQueue that only consensus
  // drains.
  run waitCycleTest =
    firstCompactBlock
      .then(envCompactBlock("p2", 1, "A"))
      .expect(prop.unverified.get("p2").has)
      .then(envVote)
      .then(syncTakeProp)
      .then(envCompactBlock("p2", 0, "A"))
      .then(recvSendProp)
      .then(envEnterRound(1, "p2"))
      .then(consApply)
      .expect(cons.pc == "sendProp" and deadlocked == Set("cons", "sync") and not(S1_noWaitCycle))
      // consensus cannot send (proposalChan full), syncData cannot push
      // (peerMsgQueue full), and consensus (parked) cannot drain peerMsgQueue
      .expect(propChan.length() == CAP_PROP and pmq.length() == CAP_PMQ and syncd.pc == "push" and cons.pc != "idle")

  // a conflicting compact block (B) at an occupied round is rejected even
  // without the fix: no forward, no gossip.
  run conflictRejectedTest =
    firstCompactBlock
      .then(envCompactBlock("p2", 0, "B"))
      .expect(recv.pc == "idle" and hist.fwd == Set((0, "A")) and prop.gossiped == Set((0, "A")) and S3_decision)
}

// only half (a): forward once, delivery still synchronous
module forwardOnceOnlyTest {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = true, ASYNC_DELIVERY = false, TRACE = true, TRACK_REQUESTS = true, PARTS = true, LOG_CAP = 6
  ).* from "./propagation"

  // without duplicates: syncData took a part and waits for cs.rsMtx, the
  // first proposal still sits in proposalChan, and consensus blocks sending
  // the cached round-1 proposal while it holds cs.rsMtx.
  run rsMtxCycleTest =
    init
      .then(envCompactBlock("p1", 0, "A"))
      .then(recvSendProp)
      .then(recvGossip)
      .then(recvSendPart)
      .then(envCompactBlock("p2", 1, "A"))
      .then(syncTakePart)
      .then(envEnterRound(1, "p2"))
      .then(consApply)
      .expect(deadlocked == Set("cons", "sync") and S2_forwardOnce)
}

// only half (b): async delivery, duplicates still forwarded
module asyncOnlyTest {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = false, ASYNC_DELIVERY = true, TRACE = true, TRACK_REQUESTS = true, PARTS = true, LOG_CAP = 6
  ).* from "./propagation"

  run asyncOnlyDuplicateTest =
    init
      .then(envCompactBlock("p1", 0, "A"))
      .then(recvSendProp)
      .then(recvGossip)
      .then(recvSendPart)
      .then(syncTakeProp)
      .then(envCompactBlock("p2", 0, "A"))
      .then(recvSendProp)
      .expect(not(S2_forwardOnce) and S1_noWaitCycle)
}

// ab6a3df applied
module fixedTest {
  import propagation(
    K = 4, MEMPOOL = 1, CAP_PROP = 1, CAP_PART = 1, CAP_PMQ = 1, MAX_ROUND = 1,
    BLOCKS = Set("A", "B", "X"), COMMITS = Set("A", "B"), VOTES = true,
    FORWARD_ONCE = true, ASYNC_DELIVERY = true, TRACE = true, TRACK_REQUESTS = true, PARTS = true, LOG_CAP = 6
  ).* from "./propagation"

  run firstCompactBlock =
    init
      .then(envCompactBlock("p1", 0, "A"))
      .then(recvSendProp)
      .then(recvGossip)
      .then(recvSendPart)

  // the duplicate is not forwarded
  run noDuplicateForwardTest =
    firstCompactBlock
      .then(syncTakeProp)
      .then(envCompactBlock("p2", 0, "A"))
      .expect(recv.pc == "idle" and S2_forwardOnce and hist.fwd == Set((0, "A")))

  // the PROTOCO-3430 inputs: consensus returns at once and a delivery
  // goroutine carries the cached proposal; no wait-for cycle.
  run noWaitCycleTest =
    firstCompactBlock
      .then(envCompactBlock("p2", 1, "A"))
      .then(envVote)
      .then(syncTakeProp)
      .then(envCompactBlock("p2", 0, "A"))
      .then(envEnterRound(1, "p2"))
      .then(consApply)
      .expect(cons.pc == "idle" and deliv.get(1).pc == "sendProp" and S1_noWaitCycle)
      // consensus drains peerMsgQueue, syncData pushes, delivery proceeds
      .then(consHandle)
      .then(syncPush)
      .then(syncTakePart)
      .then(delivSendProp(1))
      .expect(S1_noWaitCycle and S2_forwardOnce and hist.fwd == Set((0, "A"), (1, "A")))

  // the forward claim covers the cached path too: a compact block for the
  // round consensus just entered, processed between the cache pick and its
  // application, is forwarded once in total.
  run raceCachedAndLiveTest =
    init
      .then(envCompactBlock("p2", 1, "A"))
      .then(envEnterRound(1, "p2"))
      .then(envCompactBlock("p1", 1, "A"))
      .then(recvSendProp)
      .then(consApply)
      .expect(deliv.get(1).pc == "recover" and S2_forwardOnce)

  // an AddCommitment stub binds the round to a part set header: A is
  // forwarded once through it; B (another header) is rejected.
  run commitmentStubTest =
    init
      .then(envAddCommitment(0, "A"))
      .expect(prop.entries.get(0).kind == "stub" and prop.round == 0)
      .then(envCompactBlock("p1", 0, "A"))
      .then(recvSendProp)
      .expect(recv.pc == "idle" and hist.fwd == Set((0, "A")))
      .then(envCompactBlock("p2", 0, "B"))
      .expect(recv.pc == "idle" and hist.fwd == Set((0, "A")) and S3_decision)

  // honest catch-up on a stub: retryWants asks for the originals, p1
  // answers with proofs, and the node holds the block.
  run catchupCompletesTest =
    init
      .then(envAddCommitment(0, "A"))
      .then(envRetry)
      .then(envPart("p1", 0, "orig", false, true)).then(recvSendPart).then(syncTakePart).then(syncReadRs).then(syncPush).then(consHandle)
      .then(envPart("p1", 0, "orig", false, true)).then(recvSendPart).then(syncTakePart).then(syncReadRs).then(syncPush).then(consHandle)
      .then(envPart("p1", 0, "orig", false, true)).then(recvSendPart).then(syncTakePart).then(syncReadRs).then(syncPush).then(consHandle)
      .then(envPart("p1", 0, "orig", false, true)).then(recvSendPart)
      .expect(holdsBlock)

  // corrupt or unrequested parts are dropped
  run badPartsDroppedTest =
    firstCompactBlock
      .then(envPart("p2", 0, "orig", false, true))
      .expect(prop.entries.get(0).orig == Set(0))
      .then(envRetry)
      .then(envPart("p2", 0, "orig", true, true))
      .expect(prop.entries.get(0).orig == Set(0) and recv.pc == "idle")

  // any K of the 2K parts decode a full entry: one original (mempool) plus
  // three parity parts rebuild the other three originals.
  run decodeTest =
    firstCompactBlock
      .then(envHave("p1", 0, "parity"))
      .then(envPart("p1", 0, "parity", false, false))
      .then(envPart("p1", 0, "parity", false, false))
      .then(envPart("p1", 0, "parity", false, false))
      .expect(holdsBlock and recv.pc == "sendParts" and recv.n == 3)
}
