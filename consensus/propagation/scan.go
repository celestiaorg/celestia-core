package propagation

import (
	"fmt"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/cometbft/cometbft/crypto/merkle"
	"github.com/cometbft/cometbft/types"
)

// A legitimate compact block fits at most this many transaction metadata
// entries in a channel message: each includes a 32-byte transaction hash.
const maxCompactBlockBlobs = maxMsgSize / 36

// validatePropagationBytes scans repeated fields without allocating protobuf
// objects. The protobuf decoder otherwise allocates an object for every entry
// before the reactor can validate the resulting message.
func validatePropagationBytes(b []byte) error {
	var blobs, hashes, parts, aunts int
	for len(b) > 0 {
		field, wireType, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if wireType != protowire.BytesType || (field != 1 && field != 2 && field != 4) {
			n = protowire.ConsumeFieldValue(field, wireType, b)
			if n < 0 {
				return protowire.ParseError(n)
			}
			b = b[n:]
			continue
		}
		payload, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		var err error
		switch field {
		case 1:
			err = countPropagationFields(payload, 2, 6, &blobs, &hashes)
		case 2:
			err = countPropagationFields(payload, 3, 0, &parts, nil)
		case 4:
			err = countRecoveryPartAunts(payload, &aunts)
		}
		if err != nil {
			return err
		}
		if blobs > maxCompactBlockBlobs || hashes > 2*int(types.MaxBlockPartsCount) || parts > 2*int(types.MaxBlockPartsCount) || aunts > merkle.MaxAunts {
			return fmt.Errorf("propagation message has too many repeated entries")
		}
	}
	return nil
}

func countPropagationFields(b []byte, first, second protowire.Number, firstCount, secondCount *int) error {
	for len(b) > 0 {
		field, wireType, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if field == first && wireType == protowire.BytesType {
			(*firstCount)++
		} else if secondCount != nil && field == second && wireType == protowire.BytesType {
			(*secondCount)++
		}
		n = protowire.ConsumeFieldValue(field, wireType, b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
	}
	return nil
}

// countRecoveryPartAunts counts the aunts in every proof of a RecoveryPart.
func countRecoveryPartAunts(b []byte, aunts *int) error {
	for len(b) > 0 {
		field, wireType, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if field != 5 || wireType != protowire.BytesType {
			n = protowire.ConsumeFieldValue(field, wireType, b)
			if n < 0 {
				return protowire.ParseError(n)
			}
			b = b[n:]
			continue
		}
		proof, n := protowire.ConsumeBytes(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		if err := countPropagationFields(proof, 4, 0, aunts, nil); err != nil {
			return err
		}
	}
	return nil
}
