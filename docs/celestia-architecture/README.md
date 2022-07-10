---
order: 1
parent:
  order: false
---

# Tendermint and Celestia

celestia-core is not meant to be used as a general purpose framework.
Instead, its main purpose is to provide certain components (mainly consensus but also a p2p layer for Tx gossiping) for the Celestia main chain.
Hence, we do not provide any extensive documentation here.

Instead of keeping a copy of the Tendermint documentation, we refer to the existing extensive and maintained documentation and specification:

- <https://docs.tendermint.com/>
- <https://github.com/tendermint/tendermint/tree/master/docs/>
- <https://github.com/tendermint/spec>

Reading these will give you a lot of background and context on Tendermint which will also help you understand how celestia-core and [celestia-app](https://github.com/celestiaorg/celestia-app) interact with each other.

## Celestia

As mentioned above, celestia-core aims to be more focused on the Celestia use-case than vanilla Tendermint.
Moving forward we might provide a clear overview on the changes we incorporated.
For now, we refer to the Celestia specific ADRs in this repository as well as to the Celestia specification:

- [celestia-specs](https://github.com/celestiaorg/celestia-specs)

## Architecture Decision Records (ADR)

This is a location to record all high-level architecture decisions in this repository.

You can read more about the ADR concept in this [blog post](https://product.reverb.com/documenting-architecture-decisions-the-reverb-way-a3563bb24bd0#.78xhdix6t).

An ADR should provide:

- Context on the relevant goals and the current state
- Proposed changes to achieve the goals
- Summary of pros and cons
- References
- Changelog

Note the distinction between an ADR and a spec. The ADR provides the context, intuition, reasoning, and
justification for a change in architecture, or for the architecture of something
new. The spec is much more compressed and streamlined summary of everything as
it stands today.

If recorded decisions turned out to be lacking, convene a discussion, record the new decisions here, and then modify the code to match.

Note the context/background should be written in the present tense.

To start a new ADR, you can use this template: [adr-template.md](./adr-template.md)

### Table of Contents

- [ADR 001: Erasure Coding Block Propagation](./adr-001-block-propagation.md)
- [ADR 002: Sampling erasure coded Block chunks](./adr-002-ipld-da-sampling.md)
- [ADR 003: Retrieving Application messages](./adr-003-application-data-retrieval.md)
- [ADR 004: Data Availability Sampling Light Client](./adr-004-mvp-light-client.md)
- [ADR 005: Decouple BlockID and PartSetHeader](./adr-005-decouple-blockid-and-partsetheader.md)
- [ADR 006: Row Propagation](./adr-006-row-propagation.md)
- [ADR 007: Minimal Changes to Tendermint](./adr-007-minimal-changes-to-tendermint.md)
- [ADR 008: Updating to Tendermint v0.35.x](./adr-008-updating-to-tendermint-v0.35.x.md)
