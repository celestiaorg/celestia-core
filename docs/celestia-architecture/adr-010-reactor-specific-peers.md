# ADR 010: Reactor Specific Peers

## Changelog

- 28.05.2025: Initial creation of Reactor Specific Peers ADR.
- 24.06.2025: Update ADR after with learnings from initial development.

## Context

Currently, the `Switch` component handles all peer connections and interactions with reactors in a centralized manner. Peers are shared across all reactors, and the same logic applies to every reactor during operations such as **peer addition**, **removal**, or **broadcasting**.

However, there are cases where specific reactors may need to operate on different subsets of the peer set. For example:

1. A consensus reactor might need persistent connections with validators but not with non-validator nodes.
2. A state sync reactor might only require peers currently engaged in state synchronization.

The current design does not allow such flexibility, as all reactors share the same peer set managed by the `Switch`. Separating peer management by reactor would unlock enhanced flexibility and scalability without introducing unnecessary complexity.

## Alternative Approaches

### Centralized Peer Set

Keep the peer set centralized in the `Switch` and rely on reactor-specific conditions or tags for filtering:

- **Pros**:
    - No additional component or API (`PeerManager`) needed.
    - Reduces coordination between the `Switch` and other components.
- **Cons**:
    - Reactors depend on global filtering logic, which adds complexity for handling peer subsets.
    - Centralizing conditions reduces flexibility and may lead to inefficiency.

### Reactor-Defined Policies with PeerManager Component

Allow reactors to implement custom logic for peer lifecycle management independently with a dedicated `PeerManager` component:

- **Pros**:
    - Greater independence for reactors.
    - Reactors control their own state and subset of peers directly.
    - Clear separation of concerns with dedicated peer management component.
- **Cons**:
    - Duplicates common lifecycle logic and peer management tasks across reactors.
    - No centralized enforcement of connection policies, leading to inconsistencies.
    - Requires significant refactoring across the entire codebase.
    - Introduces complex coordination between `PeerManager` and `Switch`.

### Minimal Invasive Approach (Chosen Implementation)

Extend the existing `Switch` with per-channel peer sets and reactor-specific peer acceptance logic:

- **Pros**:
    - Minimal changes to existing architecture.
    - Backward compatible with existing reactors.
    - Infrastructure-first approach enables future enhancements.
    - No new components required.
- **Cons**:
    - Limited initial differentiation of peer sets.
    - Requires reactors to opt-in to selective peer management.

## Decision

This change introduces **reactor-specific peer sets** by extending the existing `Switch` component with per-channel peer management and reactor-specific peer acceptance logic. This enables reactors to maintain and interact with a **reactor-specific subset** of peers while maintaining backward compatibility.

### Chosen Solution: Minimal Invasive Approach

**Architecture Decision: Extend Existing Switch vs New PeerManager Component**

We chose to extend the existing `Switch` component rather than introducing a new `PeerManager` component for the following reasons:

**Benefits of Extension**:

1. **Minimal Disruption**: Leverages existing architecture without requiring changes across the entire codebase.
2. **Backward Compatibility**: Existing reactors continue to work without modification.
3. **Infrastructure-First**: Provides the capability for selective peer management without forcing immediate behavioral changes.
4. **Gradual Migration**: Reactors can opt-in to selective peer management as needed.
5. **Simplified Testing**: Changes are contained within the existing P2P layer.

**Implementation Details**:

- The `Switch` now maintains `peerSetByChID map[byte]*PeerSet` to track peers per channel
- Reactors can reject peers during `AddPeer()` by returning an error
- Broadcasting is isolated per reactor using `peersForEnvelope()`
- **Reference counting mechanism**: Tracks how many reactor peer sets contain each peer
- **Conditional peer disconnection**: Only drops connections when reference count reaches zero
- **Graceful peer removal**: Reactors can remove peers from their sets without affecting other reactors
- **Error-based immediate disconnection**: Bypasses reference counting for error conditions

#### Key Features

- **Per-channel peer sets**: Each reactor's channels get their own peer set
- **Reactor-specific peer acceptance**: Reactors can implement custom logic in `AddPeer()` to accept or reject peers
- **Isolated broadcasting**: Messages are only sent to peers that belong to the specific reactor's peer set
- **Graceful peer rejection**: The switch handles peer rejections gracefully without breaking the connection
- **Smart peer removal**: Peers are only disconnected when no reactors need them or on error conditions

## Detailed Design

### Peer Removal Logic

The peer removal logic implements a reference-counting approach to determine when to actually disconnect peers:

#### Removal Scenarios

1. **Error-based Removal**:
   - When a peer encounters an error (network issues, protocol violations, etc.)
   - The peer is immediately removed from all reactor peer sets
   - The underlying connection is dropped regardless of other reactors' needs

2. **Graceful Reactor Removal**:
   - When a reactor no longer needs a peer (e.g., state sync completed, peer limit reached)
   - The peer is removed from that reactor's peer set only
   - The underlying connection is maintained if other reactors still have the peer in their sets
   - Only when the peer is removed from ALL reactor peer sets is the connection dropped

3. **Reactor Shutdown**:
   - When a reactor is stopped, all its peer references are removed
   - Peers are only disconnected if no other reactors reference them

#### Implementation Requirements

- **Reference Counting**: Track how many reactors have each peer in their peer sets
- **Conditional Disconnection**: Only drop the connection when reference count reaches zero
- **Error Handling**: Immediate disconnection on errors, bypassing reference counting
- **Reactor Coordination**: Ensure reactors can independently remove peers without affecting others

### Reactor-Specific Peer Requirements

This section defines which reactors require which types of peers and the behavior for existing reactors:

#### Reactor Peer Requirements

1. **Consensus Reactor**:
   - **Peer Types**: Validator nodes, full nodes participating in consensus
   - **Criteria**: Nodes with validator keys or nodes that relay consensus messages
   - **Connection Style**: persistent (for validator connections)

2. **State Sync Reactor**:
   - **Peer Types**: Full nodes with complete state, archive nodes
   - **Criteria**: Nodes advertising state sync capability
   - **Connection Style**: ad hoc / during sync
   - **Network Requirements**: Requires larger amounts of peers to saturate network connection for faster state synchronization

3. **Block Sync Reactor**:
   - **Peer Types**: Full nodes, archive nodes with historical blocks
   - **Criteria**: Nodes with block history and fast sync capability
   - **Connection Style**: ad hoc / during sync
   - **Network Requirements**: Requires larger amounts of peers to saturate network connection for efficient block downloading

4. **Mempool Reactor**:
   - **Peer Types**: All node types that accept transactions
   - **Criteria**: None
   - **Connection Style**: ad hoc

5. **Evidence Reactor**:
   - **Peer Types**: Validator nodes, full nodes
   - **Criteria**: Nodes participating in consensus
   - **Connection Style**: ad hoc

#### Backward Compatibility for Existing Reactors

- **Default Behavior**: Reactors that don't specify peer criteria will receive all available peers (current behavior).
- **Opt-in Migration**: Existing reactors can gradually adopt peer-specific criteria without breaking changes.
- **Interface Compatibility**: Current reactor interfaces (`AddPeer`, `RemovePeer`) remain unchanged, with `AddPeer` now returning an error.

### Workflow

1. **Peer Addition**:
    - The transport layer notifies the `Switch` when a new peer is connected.
    - The `Switch` attempts to add the peer to each reactor via `AddPeer()`.
    - If a reactor returns an error from `AddPeer()`, the peer is not added to that reactor's peer set.
    - The peer is only added to peer sets of reactors that successfully accept it.

2. **Reactor-Specific Peer Interaction**:
    - Each reactor interacts only with its assigned subset of peers.
    - Reactors may independently reject peers during the connection process.

3. **Broadcasting**:
    - When broadcasting messages, the `Switch` uses `peersForEnvelope()` to target only peers within the corresponding reactor's peer set.

4. **Reconnection and Removal**:
    - The `Switch` continues to handle reconnection, persistent peers, and termination.
    - If a peer is removed, it is removed from all reactor peer sets.
    - **Reference Counting**: The switch maintains a reference count for each peer across all reactor peer sets.
    - **Graceful Removal**: When a reactor removes a peer from its set, the reference count is decremented. The connection is only dropped when the count reaches zero.
    - **Error-based Removal**: On peer errors, the peer is immediately removed from all reactor sets and the connection is dropped.
    - **Reactor Shutdown**: When a reactor stops, all its peer references are removed, potentially triggering disconnections for peers no longer needed by any reactor.

### Systems Affected

- The `Switch` now maintains per-channel peer sets alongside the global peer set.
- All reactor implementations have updated `AddPeer()` signatures to return errors.
- Broadcasting logic is modified to use reactor-specific peer sets.

### API Changes

- `AddPeer(peer Peer)` → `AddPeer(peer Peer) error` in the Reactor interface
- New `peersForEnvelope(e Envelope) []Peer` method in Switch
- Enhanced `TxStatus` RPC endpoint with `REJECTED` status for mempool transactions
- **New peer removal methods**: Reactors can call `RemovePeerFromReactor(peer Peer)` to gracefully remove a peer from their set without affecting other reactors
- **Reference counting API**: Internal methods to track peer references across reactor peer sets
- **Error-based removal**: Existing `StopPeerForError(peer Peer, reason interface{})` continues to work but now immediately drops connections

### Efficiency Considerations

- Minimal performance overhead due to maintaining both global and per-channel peer sets.
- Enhanced filtering enables reactors to scale efficiently by avoiding overhead caused by irrelevant peers.

### Observability

- Peer management metrics (e.g., peer rejections, reactor-specific peer counts) are tracked within the existing metrics system.

## Status

**Partialy implemented**

## Consequences

### Positive

1. **Reactor-Specific Peer Sets**:
    - Reactors can define and operate on different subsets of peers without affecting other reactors.
2. **Backward Compatibility**:
    - Existing reactors continue to work without modification.
3. **Infrastructure-First Approach**:
    - Provides the capability for selective peer management without forcing immediate behavioral changes.
4. **Flexibility**:
    - Reactors gain independence to operate on peers that make sense for their specific functions.
5. **Minimal Disruption**:
    - Leverages existing architecture without requiring changes across the entire codebase.
6. **Efficient Resource Management**:
    - Reference counting prevents unnecessary peer disconnections when multiple reactors share peers.
7. **Graceful Degradation**:
    - Reactors can independently manage their peer relationships without affecting others.

### Negative

1. **Limited Initial Differentiation**:
    - All peer sets are currently identical as most reactors accept all peers.
2. **Dual Peer Set Maintenance**:
    - Both global and per-channel peer sets are maintained, adding some complexity.
3. **Opt-in Nature**:
    - Reactors must explicitly implement selective peer management to benefit from the feature.
4. **Reference Counting Complexity**:
    - Additional logic required to track peer references across reactor sets.
5. **Peer Removal Coordination**:
    - Need to ensure reactors can safely remove peers without race conditions.

### Neutral

- The flexibility provided by this approach may not be needed by all reactors upfront. However, it lays a foundation for future scaling and more sophisticated peer management strategies.

## References
