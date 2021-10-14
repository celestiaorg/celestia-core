# ADR 008: Message Inclusion Check

## Changelog

- 14-10-2021: Initial Discussion

## Context

We do not currently check to ensure that the proposed block actually includes the messages that are paid for. This check is needed to ensure that blocks are actually valid. We could implement this check many different ways depending on which factors we want to optimize for, which mainly comprise of how soon we need this feature. This essentially results in three different options. Implement the check in celestia-core, in celestia-app, and in celestia-app but wait for ABCI++.

## Alternative Approaches

### Implement in core

It would be possible to have a consensus level check in celestia-core. Even though the messages that we need to parse are defined in celestia-app, and therefore we would be unable to import them, we could define an interface that would help us parse `SignedPayForMessage`s and extract the commitments and any other data needed to check for the inclusion of messages. From there we could ensure that the message committed to by the SignedPayForMessage is included in the original data square, or reject the block.

The benefits would be that we could include this check sooner rather than later, because we wouldn’t have to wait for ABCI++ or perform some medium term arguably hacky solutions in the app to accommodate the check there. However, we likely don’t need this check before devnet so that potential benefit might not be as relevant. Implementing this check inside of celestia-core will also allow us to avoid recomputing the shares, something that we will also already have to repeat in celestia-node. Notably, this approach would go against our efforts to treat tendermint like a black box. As we’ve discovered over the last few months, tendermint’s main goal is not to be modular and easily modified. That means that implementing this check in core could impose compounding long term maintenance costs, should we have to go this route. 

If we don’t want to make those tradeoffs, then we can add the check in the app instead. Should we pursue that approach, we also have a few different options in the exact mechanism. 

### Implement in the app

If we end up not having the luxury of waiting for ABCI++, then we can use tendermint’s current deferred execution model to our advantage, and not perform a consensus check at all. Instead of having a consensus check, we would perform a check on the original data square while `DeliverTx` processes a `SignedPayForMessage`. If that check shows that the expected commitment for a given `SignedPayForMessage` does not exist in the original data square, then we simply ignore that `SignedPayForMessage`. It’s just wasted block space at that point, and has no effect on the state. While this approach would be very easy to implement, it is far from ideal, as it would make concise fraud proofs much more difficult. This is because concise message inclusion fraud proofs rely on the assumption that if a `SignedPayForMessage`s exists in the original data square, but it’s corresponding message does not, then the block is invalid. This would not be the case with this approach, we could have `SignedPayForMessage`s without their corresponding message and the block would still be valid. Not only would we lose the ability to make concise message inclusion fraud proofs, but this approach would also need a way to pass the messages to the application.

### Implement in the app after ABCI++

Lastly, if we can wait for ABCI++, then we can create a solution that allows us to continue treating tendermint to a black box. During the [`ProcessProposal`](https://github.com/sikkatech/spec/blob/baf8f316093868b151491a62cf4ed3d509a7aa6a/rfc/004-abci%2B%2B.md#process-proposal) phase, we can reject blocks whose commitments in `SignedPayForMessage` do not match perfectly with the expected block data. This way we avoid the downsides of the previous two approaches discussed above, where concise message inclusion fraud proofs are still possible and we can keep tendermint as a black box. This approach would also be easy to implement *after* we adapt the celestia-core, the sdk, and the app to use ABCI++. As a meaningful bonus, this approach would even allow for us to stop re computing the shares in the data square multiple times. Using ABCI++ is definitely something that we will eventually incorporate into celestia, so if we can afford to wait, this approach would be the most ideal.


## Decision

We still need to discuss. It would not be unreasonable to implement two of the above solutions. One as a temporary fix, and then wait for ABCI++ to arrive. 

## Detailed Design

still need to decide. note that the ABCI++ version of celestia-app will be a huge overall and I don't think here is the best place to discuss.

## Status

Proposed

## References

closes [#520](https://github.com/celestiaorg/celestia-core/issues/520)

abci++ tracking issue [#60666](https://github.com/tendermint/tendermint/issues/6066) and [branch](https://github.com/tendermint/tendermint/tree/abci+%2B)
