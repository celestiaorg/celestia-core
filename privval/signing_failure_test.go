package privval

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/go-kit/kit/metrics"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/crypto/tmhash"
	cmtrand "github.com/cometbft/cometbft/libs/rand"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/types"
)

func TestFailureReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "remote signer error",
			err:  &RemoteSignerError{Code: 1, Description: "boom"},
			want: failureReasonRemoteError,
		},
		{
			name: "wrapped remote signer error",
			err:  fmt.Errorf("sign failed: %w", &RemoteSignerError{Code: 1, Description: "boom"}),
			want: failureReasonRemoteError,
		},
		{
			name: "read timeout",
			err:  fmt.Errorf("read: %w", ErrReadTimeout),
			want: failureReasonTimeout,
		},
		{
			name: "write timeout",
			err:  fmt.Errorf("write: %w", ErrWriteTimeout),
			want: failureReasonTimeout,
		},
		{
			name: "connection timeout",
			err:  ErrConnectionTimeout,
			want: failureReasonTimeout,
		},
		{
			name: "no connection",
			err:  fmt.Errorf("endpoint is not connected: %w", ErrNoConnection),
			want: failureReasonNoConnection,
		},
		{
			name: "unexpected response",
			err:  ErrUnexpectedResponse,
			want: failureReasonUnexpectedResponse,
		},
		{
			name: "other",
			err:  errors.New("something else"),
			want: failureReasonOther,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, failureReason(tc.err))
		})
	}
}

func TestRecordSigningFailureIncrementsCounter(t *testing.T) {
	failures := newCountingCounter()
	metrics := NopMetrics()
	metrics.SigningFailuresTotal = failures

	sc := &SignerClient{metrics: metrics}
	sc.recordSigningFailure(messageTypeProposal, ErrNoConnection)
	sc.recordSigningFailure(messageTypePrevote, fmt.Errorf("%w", ErrReadTimeout))
	sc.recordSigningFailure(messageTypeRawBytes, &RemoteSignerError{Code: 1, Description: "x"})

	require.Equal(t, float64(1), failures.countFor(messageTypeProposal, failureReasonNoConnection))
	require.Equal(t, float64(1), failures.countFor(messageTypePrevote, failureReasonTimeout))
	require.Equal(t, float64(1), failures.countFor(messageTypeRawBytes, failureReasonRemoteError))
}

func TestSignerClientRecordsSigningFailureOnRemoteError(t *testing.T) {
	for _, tc := range getSignerTestCases(t) {
		tc := tc
		t.Cleanup(func() {
			if err := tc.signerServer.Stop(); err != nil {
				t.Error(err)
			}
		})
		t.Cleanup(func() {
			if err := tc.signerClient.Close(); err != nil {
				t.Error(err)
			}
		})

		failures := newCountingCounter()
		metrics := NopMetrics()
		metrics.SigningFailuresTotal = failures
		tc.signerClient.SetMetrics(metrics)

		tc.signerServer.privVal = types.NewErroringMockPV()

		ts := time.Now()
		hash := cmtrand.Bytes(tmhash.Size)
		proposal := &types.Proposal{
			Type:      cmtproto.ProposalType,
			Height:    1,
			Round:     2,
			POLRound:  2,
			BlockID:   types.BlockID{Hash: hash, PartSetHeader: types.PartSetHeader{Hash: hash, Total: 2}},
			Timestamp: ts,
			Signature: []byte("signature"),
		}

		err := tc.signerClient.SignProposal(tc.chainID, proposal.ToProto())
		require.Error(t, err)
		require.Equal(t, float64(1), failures.countFor(messageTypeProposal, failureReasonRemoteError))
		// Failed signs must not enter the success latency window.
		require.Equal(t, 0, tc.signerClient.latency.size)
	}
}

type countingCounter struct {
	state *countingCounterState
	vals  []string
}

type countingCounterState struct {
	mtx    sync.Mutex
	counts map[string]float64
}

func newCountingCounter() *countingCounter {
	return &countingCounter{
		state: &countingCounterState{counts: make(map[string]float64)},
	}
}

func (c *countingCounter) With(labelValues ...string) metrics.Counter {
	vals := make([]string, 0, len(c.vals)+len(labelValues))
	vals = append(vals, c.vals...)
	vals = append(vals, labelValues...)
	return &countingCounter{state: c.state, vals: vals}
}

func (c *countingCounter) Add(n float64) {
	c.state.mtx.Lock()
	defer c.state.mtx.Unlock()
	key := labelsKey(c.vals)
	c.state.counts[key] += n
}

func (c *countingCounter) countFor(messageType, reason string) float64 {
	c.state.mtx.Lock()
	defer c.state.mtx.Unlock()
	return c.state.counts[labelsKey([]string{"message_type", messageType, "reason", reason})]
}

func labelsKey(vals []string) string {
	out := ""
	for i := 0; i < len(vals); i += 2 {
		if i > 0 {
			out += ","
		}
		out += vals[i] + "=" + vals[i+1]
	}
	return out
}
