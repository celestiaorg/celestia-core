# Consensus Reactor

Consensus reactor handles message propagation for 4 different channels, namely, `StateChannel`, `DataChannel`, `VoteChannel`, and `VoteSetBitsChannel`.
The focus of this document is on the `DataChannel` and also covers the relevant parts of the `StateChannel`.

## Message Types

We will refer to the following message types in the following sections.

### Part

The `Part` serves as a representation for a block part.
Its `bytes` field is constrained to a maximum size of [64kB](https://github.com/celestiaorg/celestia-core/blob/5a7dff4f3a5f99a4a22bb8a4528363f733177a2e/types/params.go#L19).
`Proof` is the Merkle inclusion proof of the block part in the block (it is the proof of its inclusion in the Merkle root `Hash` found in the `PartSetHeader` of that particular block)
```go
type Part struct {
Index uint32       `protobuf:"varint,1,opt,name=index,proto3" json:"index,omitempty"`
Bytes []byte       `protobuf:"bytes,2,opt,name=bytes,proto3" json:"bytes,omitempty"`
Proof crypto.Proof `protobuf:"bytes,3,opt,name=proof,proto3" json:"proof"`
}
```

### Block Part

A `BlockPart` encapsulates a block part as well as the height and round of the block. 
```go
// BlockPart is sent when gossipping a piece of the proposed block.
type BlockPart struct {
	Height int64      `protobuf:"varint,1,opt,name=height,proto3" json:"height,omitempty"`
	Round  int32      `protobuf:"varint,2,opt,name=round,proto3" json:"round,omitempty"`
	Part   types.Part `protobuf:"bytes,3,opt,name=part,proto3" json:"part"`
}
```

### Part Set Header

A `PartSetHeader` contains the metadata about a block part set.
`Total` is the total number of parts in the block part set, and
`Hash` is the Merkle root of the block parts.
.
```go
type PartSetHeader struct {
	Total uint32            `json:"total"` 
	Hash  cmtbytes.HexBytes `json:"hash"`
}
```
### Proposal

A `Proposal` is a representation of a block proposal.
```go
type Proposal struct {
	Type      cmtproto.SignedMsgType
	Height    int64     `json:"height"`
	Round     int32     `json:"round"`     // there can not be greater than 2_147_483_647 rounds
	POLRound  int32     `json:"pol_round"` // -1 if null.
	BlockID   BlockID   `json:"block_id"`
	Timestamp time.Time `json:"timestamp"`
	Signature []byte    `json:"signature"`
}
```

### Peer Round State
[`PeerRoundState`](https://github.com/celestiaorg/celestia-core/blob/4b925ca55acc75d51098a7e02ea1e3abeb9bab76/consensus/types/peer_round_state.go#L15) is used to represent the known state of a peer.
Many fields are omitted for brevity.
```go
type PeerRoundState struct {
	Height int64         `json:"height"` // Height peer is at
	Round  int32         `json:"round"`  // Round peer is at, -1 if unknown.
	Step   RoundStepType `json:"step"`   // Step peer is at
	

	// True if peer has proposal for this round and height
	Proposal                   bool                `json:"proposal"`
	ProposalBlockPartSetHeader types.PartSetHeader `json:"proposal_block_part_set_header"`
	ProposalBlockParts         *bits.BitArray      `json:"proposal_block_parts"`
}
```

## Data Channel

Block proposals are divided into smaller parts called Block Parts, or `BlockPart`. 
The `DataChannel` protocol, adopts a push-based approach, and distributes these `BlockPart`a and block proposals, termed `Proposal`, to network peers.
The determination of which data to relay to a particular peer hinges on that peer's status, such as its height, round, and the block proposal observed by it.

Peers state information is updated via another protocol operating within a distinct channel, namely, `StateChannel`.
The state of a peer, designated as `PeerRoundState`, is periodically updated through a push-based protocol functioning within the `StateChannel`.
This refreshed state guides the decision on the type of data to be sent to the peer on the `DataChannel`.


The `DataChannel` protocol is articulated in two separate sections: 
the first elucidates the _gossiping procedure_, while the second delves into the _receiving procedure_.

### Gossiping Procedure

For every peer connected to a node that supports the `DataChannel`, a gossiping procedure is initiated. 
This procedure is concurrent and continuously runs in an infinite loop, with one action executed in each iteration. 
During each iteration, the node captures a snapshot of the connected peer's state, denoted as [`RoundSate`](#peer-state), and then follows the steps outlined below. 
It's important to note that the peer state is regularly updated through a push-based protocol operating on a separate channel i.e., `StateChannel`.

Case1: The `ProposalBlockPartSetHeader` from the peer's state aligns with the node's own `PartSetHeader`.
Essentially, this ensures both entities are observing the identical proposal hash accompanied by an equal count of block parts.
The node randomly selects one of its block parts that hasn't been transmitted to the peer. 
If such a block part is not found, other cases are examined.
- A `BlockPart` message is dispatched to the peer under the conditions that:
  - The peer is still connected and operational.
  - The peer is subscribed to the `DataChannel`.
- The node updates the peer state to record the transmission of that block part, if:
  - The transmission does not error out.
  - The round and height of the peer remain consistent pre and post-transmission of the part. 
  References can be found [here](https://github.com/celestiaorg/celestia-core/blob/5a7dff4f3a5f99a4a22bb8a4528363f733177a2e/consensus/reactor.go#L593) and [here](https://github.com/celestiaorg/celestia-core/blob/5a7dff4f3a5f99a4a22bb8a4528363f733177a2e/consensus/reactor.go#L588).


Case2:  The peer's height is not recent rather falls within the range of the node's earliest and most recent heights.
The goal is to send a single block part corresponding to the block height the peer is syncing with.
If any internal issue or network issue happens that prevents the node from sending a block part (or the transmission fails), then the node sleeps for [`PeerGossipSleepDuration`=100ms](https://github.com/celestiaorg/celestia-core/blob/2f2dfcdb0614f81da4c12ca2d509ff72fc676161/config/config.go#L984) and reinstates the gossip procedure.
- **Initialization**: If the peer's round state lacks a header for the specified block height, the node takes the initiative to set it up. 
The node then updates the `ProposalBlockPartSetHeader` within the peer's round state with the `PartSetHeader` it recognizes for that block height.
Additionally, the `ProposalBlockParts` is initialized as an empty bit array. 
Its size is determined by the total number of parts corresponding to that block height.
- **Catch up**:
At this stage, the node randomly selects an index for a block part that it has not yet transmitted to the peer. 
Before sending it, the node performs the following checks, provided that the node possesses that part:
- It verifies whether the `PartSetHeader` for the specified height matches the `PartSetHeader` of the snapshot of the peer's round state.

If  the above check passes successfully, the node proceeds to send the `BlockPart` message to the peer through the `DataChannel`. This process assumes that:
- The peer is currently operational and running.
- The peer supports  the `DataChannel`.

If there are no issues encountered during the transmission of the `BlockPart` message, the peer is marked as having received the block part for the specific round, height, and part index, provided that its state has not changed since the block part was sent.
Following this, the node advances to the next iteration of the gossip procedure.

Case 3: If the peer's round OR height don't match
The node sleeps for [`PeerGossipSleepDuration duration`, i.e., 100 ms](https://github.com/celestiaorg/celestia-core/blob/7f2a4ad8646751dc9866370c3598d394a683c29f/config/config.go#L984) and reinstates the gossip procedure.


Case 4: The peer, which has the same height and round as the node, has not yet received the proposal. 
The node sends the `Proposal` to the peer and updates the peer's round state with the proposal if certain conditions are met:
- The current round and height of the receiving peer match the proposal's, and the peer's state hasn't been updated yet.
- If the peer's state for that proposal remains uninitialized since the proposal's transmission, the node initializes it by assigning the `ProposalBlockPartSetHeader` and an empty bit array with a size equal to the number of parts in the header for the `ProposalBlockParts`.

[//]: # (There are further parts pertaining the communication of proof of lock messages which are ommitted here.)

### Receiving messages
On the receiving side, the node performs basic message validation [reference](https://github.com/celestiaorg/celestia-core/blob/2f2dfcdb0614f81da4c12ca2d509ff72fc676161/consensus/reactor.go#L250). 
If the message is invalid, the node stops the peer (for persistent peers, a reattempt may occur).

If the node is in the fast sync state, it disregards the received message [reference](https://github.com/celestiaorg/celestia-core/blob/2f2dfcdb0614f81da4c12ca2d509ff72fc676161/consensus/reactor.go#L324).

#### Block Part Message
For `BlockPartMessage`, the node updates the peer state to indicate that the sending peer has the block part only if the round and height of the received block part message match the sending peer's round state. 
Additionally, it sends the block part to the list of parts known for the current proposal, given that:
- The receiving node's height matches the block part message's height
- The receiving node is expecting a block part (no proposal is currently being processed)
- The block part message is valid:
  - Has an index less that the total number of parts for the current proposal
  - The block part's merkle inclusion proof is valid w.r.t. the block part set hash 

[//]: # (The completion of the block proposal parts triggers the [CompleteProposal event](https://github.com/celestiaorg/celestia-core/blob/0498541b8db00c7fefa918d906877ef2ee0a3710/consensus/state.go#L1942), yet other peers don't seem to be signalled to stop gossiping further block parts of that proposal.)

#### Proposal Message
If the received message is a `Proposal` message, the node checks whether:
- The height and round of the current peer's state match the received message's height and round.
- The peer's round state hasn't been initialized yet.

If both conditions are met, the node initializes the peer's round state with the `ProposalBlockPartSetHeader` from the message and creates an empty bit array for `ProposalBlockParts` with a size equal to the number of parts in the header. 
Then, it adds the message to the `peerMsgQueue` channel for processing.

## State Channel Protocol

Peers engage in communication through the `StateChannel` to share details about their current state.
Pertinent messages for this document include:

### New Round Step Message
When a peer dispatches a `NewRoundStepMessage`, it signifies an update in its height/round/step.
The node on the receiving end takes the following actions:
- The parameters `Height`, `Round`, and `Step` of the peer's round state are updated accordingly.
- If there's a change in `Height` or `Round` compared to the previous peer state, the node reinitializes the peer state to reflect the absence of a proposal for that specific `Height` and `Round`.
  This essentially resets the `ProposalBlockParts` and `ProposalBlockPartSetHeader` within the peer's round state.

```go
// NewRoundStepMessage is sent for every step taken in the ConsensusState.
// For every height/round/step transition
type NewRoundStepMessage struct {
	Height                int64
	Round                 int32
	Step                  cstypes.RoundStepType
	SecondsSinceStartTime int64
	LastCommitRound       int32
}
```

[//]: # (The merkle hash of the proposal is not communicated in the state channel, then wondering how the two parties know they have the same proposal part set header hash befor commencing block part tranfer for a specific height and round. -->)
[//]: # (Related to the above question, what if only the round changes, but the proposal remains the same (locked)? by resetting the peer's state, we lose the history of the block parts that the peer has received, hence the same block parts may need to be sent again.)

### New Valid Block Message

A peer might send a `NewValidBlockMessage` to the node via the `StateChannel` when two third prevotes is observed for a block.
```go
// NewValidBlockMessage is sent when a validator observes a valid block B in some round r,
// i.e., there is a Proposal for block B and 2/3+ prevotes for the block B in the round r.
// In case the block is also committed, then IsCommit flag is set to true.
type NewValidBlockMessage struct {
	Height             int64
	Round              int32
	BlockPartSetHeader types.PartSetHeader
	BlockParts         *bits.BitArray
	IsCommit           bool
}
```

Upon receiving this message, the node will only modify the peer's round state under these conditions:
- The `Height` specified in the message aligns with the peer's current `Height`.
- The `Round` matches the most recent round known for the peer OR the message indicates the block's commitment i.e., `IsCommit` is `ture`.

Following these verifications, the node will then update its peer state's `ProposaBlockPartSetHeader` and `ProposaBlockParts` based on the `BlockPartSetHeader` and `BlockParts` values from the received message.

[//]: # (The BlockParts field of the message seem to represent the parts that the sending peer has received so far for this particular proposal, from all of its connectins, so this means that the receving peer becomes aware of which part that peer has, potentially sending less block parts afterwards. )

[//]: # (Does this message also signify that the sender has the entire proposal? 
or can a node send this merely based on the observed votes?  
After further investigation, it looks like that this is purely based on votes. -->)

## Network Traffic Analysis

The following section provides a detailed analysis of the network traffic generated by the `DataChannel` protocol.
Essentially, the focus is on the [`BlockPart`](#block-part) message as well as the [`Proposal`](#proposal), which are the most frequently transmitted messages in the `DataChannel` protocol.

We denote `block_part_size` as the size of a block part in bytes and `proposal_header_size` as the size of a proposal header in bytes.
Suppose `proposal(H,R)` denotes the proposal at height and round `H` and `R`, respectively.
With this notation we assume there is only one valid proposal in the network for a given round and height.
`proposal(H,R).total` denotes the number of parts in that proposal.

For every block proposal, peers start gossiping their obtained proposal and constituent block parts to their connected peers.
Both sending and receiving ends monitor the block parts they've exchanged with their counterpart (either dispatched to or received from) and mark it as seen by the other peer.
Peers keep exchanging block parts of a proposal until either 1) all block parts of the intended proposal have been successfully transmitted between the two peers or 2) one of the peer's round state updates (and points to a new height and round with a different proposal).
In the latter case, the peer whose state has advanced still sends block parts to the other peer until all the parts are transmitted or until the receiving peer's round state is also updated.

Worst Case Scenario: The worst case occurs when both peers coincidentally choose the same block part index at the same moment and initiate the sending process concurrently, a scenario that's rather unlikely.
The outcome of this is that the cumulative number of block parts transmitted between the two peers (sent and received) equals `2 * proposal(H,R).total`.
Likewise, 2 instances of the proposal header are also transmitted between the peers.

Best Case Scenario: Ideally, only one instance of each block part is exchanged between the two peers (the opposite of the worst case).
Consequently, the aggregate number of block parts transferred (both sent and received) between the peers is `proposal(H,R).total`. 
Also, only one instance of the proposal header is transmitted between the two peers.
This number can further reduce if one of the peers acquires block parts from additional connections, thereby advancing to the subsequent height, round, or proposal.
Upon receiving information about the other peer's updated state, they cease transmitting block parts to that peer.

Based on above, it can be established that one network health indicator is that the cumulative number of block parts sent and received over each p2p connection over `DataChannel` should not surpass the total block parts specified in the proposal for a particular height and round.
This should hold true even when one of the  two ends of communication lags behind and is catching up by obtaining block parts of the past blocks. 

[//]: # (TODO: will verify this by inspecting the Prometheus metrics for the number of block parts sent and received over each p2p connection. Alternatively, will develop a go test to verify this. -->)

### Questions and Optimization Ideas

1. In the event of a connection disruption, is the peer's round state reset, or does the process pick up from the last known point?
1. [Optimization idea] could other peers halt the transmission of block parts to a peer that reaches the prevote step (taking prevote step as an indication that the node must have possessed the entire block)?
