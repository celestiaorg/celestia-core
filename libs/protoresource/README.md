# Protobuf resource preflight

Compile an immutable `Profile` from the production decoder's generated descriptor
resolver during setup. `Check` scans wire bytes before decoding. `Measure` also
returns admission units; `Budget` reserves capacity until the consumer releases
its `Reservation`.

Profiles bound bytes, field/message occurrences, repeated elements, copy bytes,
recursion and weighted work across the complete envelope. Field caps count
cumulatively across duplicate and overwritten occurrences. Unknown groups follow
gogoproto's depth-based skipping semantics within the same work/depth limits.
Known group fields and message extensions require an explicitly supported policy;
profile construction rejects them.

Admission weights charge 4096 units per message, 64 per repeated element,
32 per field and two per copied byte. These are structural estimates, not exact
heap accounting. Integrations must validate the estimates against their concrete
Go types, codecs and maximum supported traffic, and choose reviewed ceilings.
Custom decoders may need additional constraints.

This package alone does not enforce any production boundary. Integrations must:

- run preflight before the production decoder and native conversions;
- cover second-stage decoding of opaque bytes and interface payloads;
- retain reservations through every asynchronous queue or transferred owner;
- bound transport buffers before decoding and release capacity on all exits;
- preserve historical/consensus acceptance or explicitly version-gate changes.

A queue consumer must not release a reservation while another owner still retains
the covered data. Each owner needs its own accounting or a reviewed ownership
transfer. The library does not make invalid inputs semantically valid and does
not replace post-decode validation.
