package propagation

import (
	"errors"
	"github.com/stretchr/testify/assert"
	proptypes "github.com/tendermint/tendermint/consensus/propagation/types"
	"github.com/tendermint/tendermint/libs/bits"
	"github.com/tendermint/tendermint/libs/log"
	"github.com/tendermint/tendermint/p2p"
	"github.com/tendermint/tendermint/p2p/mock"
	"testing"
)

func TestRequester_SendRequest(t *testing.T) {
	logger := log.NewNopLogger()
	r := newRequester(logger)

	peer := mock.NewPeer(nil)

	tests := []struct {
		name              string
		setup             func()
		want              *proptypes.WantParts
		expectedSent      bool
		expectedError     error
		expectedQueueSize int
	}{
		{
			name: "successful request",
			setup: func() {
				r.perPeerRequests = make(map[p2p.ID]int)
				r.perPartRequests = make(map[int64]map[int32]map[int]int)
				r.pendingRequests = []*request{}
			},
			want: &proptypes.WantParts{
				Height: 10,
				Round:  1,
				Parts:  bits.NewBitArray(1),
				Prove:  false,
			},
			expectedSent:      true,
			expectedError:     nil,
			expectedQueueSize: 0,
		},
		{
			name: "per-peer limit reached - sending to pending queue",
			setup: func() {
				r.perPeerRequests[peer.ID()] = concurrentPerPeerRequestLimit
			},
			want: &proptypes.WantParts{
				Height: 10,
				Round:  1,
				Parts:  bits.NewBitArray(1),
				Prove:  false,
			},
			expectedSent:      true,
			expectedError:     nil,
			expectedQueueSize: 1,
		},
		{
			name: "per-part limit reached - sending to pending queue",
			setup: func() {
				r.perPartRequests[10] = map[int32]map[int]int{
					1: {0: maxRequestsPerPart},
				}
			},
			want: func() *proptypes.WantParts {
				bitArray := bits.NewBitArray(1)
				bitArray.SetIndex(0, true)
				return &proptypes.WantParts{
					Height: 10,
					Round:  1,
					Parts:  bitArray,
					Prove:  false,
				}
			}(),
			expectedSent:      true,
			expectedError:     nil,
			expectedQueueSize: 1,
		},
		{
			name: "too many pending requests",
			setup: func() {
				r.pendingRequests = make([]*request, maxNumberOfPendingRequests)
			},
			want: &proptypes.WantParts{
				Height: 10,
				Round:  1,
				Parts:  bits.NewBitArray(1),
				Prove:  false,
			},
			expectedSent:      false,
			expectedError:     errors.New("too many pending requests"),
			expectedQueueSize: maxNumberOfPendingRequests,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup()
			sent, err := r.sendRequest(peer, tt.want)

			assert.Equal(t, tt.expectedSent, sent)
			if tt.expectedError != nil {
				assert.EqualError(t, err, tt.expectedError.Error())
			} else {
				assert.NoError(t, err)
			}
			assert.GreaterOrEqual(t, len(r.pendingRequests), tt.expectedQueueSize)
		})
	}
}
