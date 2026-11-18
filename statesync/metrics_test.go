package statesync

import (
	"strings"
	"sync"

	"github.com/go-kit/kit/metrics"
)

// countingCounter is a metrics.Counter that records the total for each set of
// label values.
type countingCounter struct {
	mtx    *sync.Mutex
	counts map[string]float64
	labels []string
}

var _ metrics.Counter = (*countingCounter)(nil)

func newCountingCounter() *countingCounter {
	return &countingCounter{mtx: &sync.Mutex{}, counts: map[string]float64{}}
}

func (c *countingCounter) With(labelValues ...string) metrics.Counter {
	return &countingCounter{
		mtx:    c.mtx,
		counts: c.counts,
		labels: append(append([]string{}, c.labels...), labelValues...),
	}
}

func (c *countingCounter) Add(delta float64) {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	c.counts[strings.Join(c.labels, ",")] += delta
}

// countFor returns the total for the given message type and reason.
func (c *countingCounter) countFor(messageType, reason string) float64 {
	c.mtx.Lock()
	defer c.mtx.Unlock()
	return c.counts[strings.Join([]string{"message_type", messageType, "reason", reason}, ",")]
}
