# ADR 010: Reactor Specific Peers

## Changelog

- 28.05.2025: Initial creation of Reactor Specific Peers ADR.

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

### Reactor-Defined Policies

Allow reactors to implement custom logic for peer lifecycle management independently:
- **Pros**:
    - Greater independence for reactors.
    - Reactors control their own state and subset of peers directly.
- **Cons**:
    - Duplicates common lifecycle logic and peer management tasks across reactors.
    - No centralized enforcement of connection policies, leading to inconsistencies.

## Decision

This change introduces a `PeerManager` component that delegates **peer lifecycle management** and allows subsets of the peer set to be assigned to individual reactors. This enables reactors to maintain and interact with a **reactor-specific subset** of peers.

### Proposed Solution: Combined Alternative

**Architecture Decision: Two-Component vs Single-Component Approach**

We chose a two-component approach (`PeerManager` + updated `Switch`) instead of consolidating everything into the `Switch` for the following reasons:

**Benefits of Separation**:
1. **Single Responsibility Principle**: The `PeerManager` focuses solely on peer lifecycle (connection, validation, assignment), while the `Switch` handles reactor coordination and message routing.
2. **Testability**: Peer management logic can be tested independently of reactor coordination logic.
3. **Future Extensibility**: The `PeerManager` can be enhanced with advanced features (peer scoring, adaptive peer selection) without cluttering the `Switch`.
4. **Clear Interface Boundaries**: Well-defined APIs between components reduce coupling.
5. **Code Reuse Potential**: With clear separation of concerns, we might be able to reuse some peer management code from libp2p libraries.

**Communication Complexity Mitigation**:
- The `PeerManager` will expose a simple interface to the `Switch` for peer assignment and retrieval operations.
- The `Switch` will delegate peer lifecycle events to `PeerManager` but retain control over message routing.

There will be two main components:
1. **PeerManager**:
    - Centrally manages the lifecycle of peers (e.g., validation, filtering, reconnection, termination).
    - Maintains the full peer set and allows subsets of peers to be defined for individual reactors.
    - Maps and tracks peer-reactor assignments based on reactor-specific criteria.

2. **Switch**:
    - Delegates peer management responsibilities to the `PeerManager`.
    - Routes reactor interactions only to their assigned peer subsets.
    - Starts and stops reactors independently, without worrying about connection states.

#### Key Features:
- Reactors can define customized criteria for which peers they interact with and operate only on their assigned subset.
- The `PeerManager` controls connection policies such as reconnection for persistent peers, filtering, and connection limits.

## Detailed Design

### Reactor-Specific Peer Requirements

This section defines which reactors require which types of peers and the behavior for existing reactors:

#### Reactor Peer Requirements:
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

#### Backward Compatibility for Existing Reactors:
- **Default Behavior**: Reactors that don't specify peer criteria will receive all available peers (current behavior).
- **Opt-in Migration**: Existing reactors can gradually adopt peer-specific criteria without breaking changes.
- **Interface Compatibility**: Current reactor interfaces (`AddPeer`, `RemovePeer`) remain unchanged.

### Workflow

1. **Peer Addition**:
    - The transport layer notifies the `PeerManager` when a new peer is connected.
    - The `PeerManager` validates, filters, and, if approved, assigns the peer to one or multiple reactors based on their criteria.

2. **Reactor-Specific Peer Interaction**:
    - Each reactor interacts only with its assigned subset of peers.
    - Reactors may independently add or remove specific peers from their subset.

3. **Broadcasting**:
    - When broadcasting messages, the `Switch` ensures only peers within the corresponding reactor's subset are targeted.

4. **Reconnection and Removal**:
    - The `PeerManager` implements policies for reconnection, persistent peers, and termination.
    - If a peer is removed, all reactors interacting with that peer are notified.

### Systems Affected
- The `Switch` must delegate peer lifecycle management to the `PeerManager`.
- Transport layers that add or remove peers will now interact with the `PeerManager` instead of the `Switch`.

### API Changes
- A new API will allow reactors to specify the subsets of peers they interact with.
- APIs for broadcasting or managing peers within the `Switch` will now use the `PeerManager`'s subset handling logic.

### Efficiency Considerations
- Minimal performance overhead due to centralized peer lifecycle logic in the `PeerManager`.
- Enhanced filtering enables reactors to scale efficiently by avoiding overhead caused by irrelevant peers.

### Observability
- Peer management metrics (e.g., reconnections, filtering) will be tracked centrally within the `PeerManager`.

## Status

**Proposed**

## Consequences

### Positive
1. **Reactor-Specific Peer Sets**:
    - Reactors can define and operate on different subsets of peers without affecting other reactors.
2. **Single Responsibility Principle (SRP)**:
    - The `PeerManager` focuses on connection and lifecycle management, freeing the `Switch` to handle reactor coordination and communication.
3. **Scalability**:
    - Clear separation of concerns simplifies scaling policies, such as dynamic peer scoring or adaptive subsets per reactor.
4. **Flexibility**:
    - Reactors gain independence to operate on peers that make sense for their specific functions.
5. **Observability**:
    - Centralized metrics and events related to peer management (e.g., reconnections, filtering) are handled by the `PeerManager`.

### Negative
1. **Implementation Overhead**:
    - Introducing the `PeerManager` and refactoring existing `Switch` responsibilities require significant effort.
2. **Coordination Complexity**:
    - Additional communication paths are necessary between the `PeerManager` and `Switch`, though mitigated by clear interface boundaries.

### Neutral
- The flexibility provided by this approach may not be needed by all reactors upfront. However, it lays a foundation for future scaling.

## References
