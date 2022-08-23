# ADR 009: InfoReservedByte

## Changelog

- 2022/9/22: inital draft

## Context

The current message share format is as follows:

- `nid (8 bytes) | message length (varint) | share data` for shares at the start of a message.
- `nid (8 bytes) | share data` for other shares.

The current message share format poses two challenges:

1. It is difficult to make changes to the message share format in a backwards compatible way because clients can't determine which version of the message share format an individual share conforms to.
1. It is not possible for a client that samples a random message share to determine if the share is the start of a message or a contiguous share for a multi-share message.

## Proposal

- `nid (8 bytes) | info byte | message length (varint) | share data` for shares at the start of a message.
- `nid (8 bytes) | info byte | share data` for other shares.

Where info byte is a byte with the following structure:

- the first 7 bits are reserved for the version information in big endian form (initially, this will just be 0000000 until further notice);
- the last bit is a "message start indicator", that is 1 if the share is at the start of a message.

Rationale:

1. The message start indicators allow clients to parse a whole message in the middle of a namespace, without needing to read the whole namespace.
1. The version bits allow us to upgrade the share format in the future, if we need to do so in such a way that different share formats can be mixed within a block.

## Alternative Approaches

// TODO

## Decision

// TODO

## Implementation Details

### Protobuf

1. Add `Version` to [`MsgWirePayForData`](https://github.com/celestiaorg/celestia-app/blob/main/proto/payment/tx.proto#L19)
1. Add `Version` to [`MsgPayForData`](https://github.com/celestiaorg/celestia-app/blob/main/proto/payment/tx.proto#L44)

**NOTE**: Protobuf does not support the byte type (see [Scalar Value Types](https://developers.google.com/protocol-buffers/docs/proto3#scalar)) so a `uint32` will be used for `Version`. Since `Version` is constrained to 2^7 bits (0 to 127 inclusive), a `Version` outside the supported range (i.e. 128) will seriealize / deserialize correctly but be considered invalid by the application. Adding this field increases the size of the message by one byte + protobuf overhead.

### Constants

1. Define a new constant for `InfoReservedBytes = 1`.
1. Update [`MsgShareSize`](https://github.com/celestiaorg/celestia-core/blob/v0.34.x-celestia/pkg/consts/consts.go#L26) to account for one less byte available

**NOTE**: Currently constants are defined in celestia-core's [consts.go](https://github.com/celestiaorg/celestia-core/blob/master/pkg/consts/consts.go) but some will be moved to celestia-app's [appconsts.go](https://github.com/celestiaorg/celestia-app/tree/evan/non-interactive-defaults-feature/pkg/appconsts). See [celestia-core#841](https://github.com/celestiaorg/celestia-core/issues/841).

### Types

1. Introduce a new type `InfoReservedByte` to encapsulate the logic around getting the `Version()` or `IsMessageStart()` from a message share.

```golang
// InfoReservedByte is a byte with the following structure: the first 7 bits are
// reserved for version information in big endian form (initially `0000000`).
// The last bit is a "message start indicator", that is `1` if the share is at
// the start of a message and `0` otherwise.
type InfoReservedByte byte

func NewInfoReservedByte(version uint8, isMessageStart bool) (InfoReservedByte, error) {
	if version > 127 {
		return 0, fmt.Errorf("version %d must be less than or equal to 127", version)
	}

	prefix := version << 1
	if isMessageStart {
		return InfoReservedByte(prefix + 1), nil
	}
	return InfoReservedByte(prefix), nil
}

// Version returns the version encoded in this InfoReservedByte.
// Version is expected to be between 0 and 127 (inclusive).
func (i InfoReservedByte) Version() uint8 {
	version := uint8(i) >> 1
	return version
}

// IsMessageStart returns whether this share is the start of a message.
func (i InfoReservedByte) IsMessageStart() bool {
	return uint(i)%2 == 1
}
```

1. Add `Version uint8` to [`Message`](https://github.com/rootulp/celestia-core/blob/master/types/block.go#L1172)

### Logic

#### celestia-core

1. Account for the new `InfoReservedByte` in `./types/share_splitting.go` and `./types/share_merging.go`.
    - **NOTE**: These files are subject to be deleted soon. See <https://github.com/celestiaorg/celestia-core/issues/842>

#### celestia-app

1. Account for the new `InfoReservedByte` in `./pkg/shares/split_message_shares.go` and `./pkg/shares/merge_message_shares.go`. There is an in-progress refactor of the relevant files. See <https://github.com/celestiaorg/celestia-app/pull/637>

## Status

Proposed

## Consequences

### Positive

This proposal resolves challenges posed above.

### Negative

This proposal reduces the number of bytes a message share can use for data by one byte.

### Neutral

If 127 versions is larger than required, the message share format spec can be updated (in a subsequent version) to reserve fewer bits for the version in order to use some bits for other purposes.

If 127 versions is smaller than required, the message share format spec can be updated (in a subsequent version) to occupy multiple bytes for the version. For example if the 7 bits are `1111111` then read the second byte.

## References

- <https://github.com/celestiaorg/celestia-core/issues/839>
- <https://github.com/celestiaorg/celestia-core/issues/759>
- <https://github.com/celestiaorg/celestia-core/issues/757>
