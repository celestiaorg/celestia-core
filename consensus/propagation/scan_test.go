package propagation

import (
	"testing"

	"github.com/cometbft/cometbft/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protowire"
)

func TestPropagationPrecheck(t *testing.T) {
	channels := (&Reactor{}).GetChannels()
	for _, channel := range channels {
		require.NotNil(t, channel.RecvMessagePrecheck)
	}

	appendEntries := func(outer, inner protowire.Number, count int) []byte {
		payload := make([]byte, 0, count*2)
		for range count {
			payload = protowire.AppendTag(payload, inner, protowire.BytesType)
			payload = protowire.AppendBytes(payload, nil)
		}
		message := protowire.AppendTag(nil, outer, protowire.BytesType)
		return protowire.AppendBytes(message, payload)
	}
	check := channels[1].RecvMessagePrecheck
	for _, tc := range []struct {
		name    string
		message []byte
		wantErr bool
	}{
		{"empty", nil, false},
		{"compact block count at limit", appendEntries(1, 6, 2*int(types.MaxBlockPartsCount)), false},
		{"excess compact block hashes", appendEntries(1, 6, 2*int(types.MaxBlockPartsCount)+1), true},
		{"excess compact block blobs", appendEntries(1, 2, maxCompactBlockBlobs+1), true},
		{"reported compact block shape", appendEntries(1, 2, 250_000), true},
		{"excess have parts", appendEntries(2, 3, 2*int(types.MaxBlockPartsCount)+1), true},
		{"malformed nested field", []byte{0x0a, 0x01, 0xff}, true},
		{"malformed outer field", []byte{0x0a, 0x05}, true},
		{"other message", appendEntries(3, 3, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.message)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
