package types

import "fmt"

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
