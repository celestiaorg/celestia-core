package trace

import (
	"errors"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
)

type Entry interface {
	Table() string
	InfluxRepr() (map[string]interface{}, error)
}

type Tracer interface {
	Write(Entry)
	ReadTable(table string) ([]byte, error)
	IsCollecting(table string) bool
	Stop()
}

func NewTracer(cfg *config.Config, logger log.Logger, chainID, nodeID string) (Tracer, error) {
	return NewInfluxClient(cfg.Instrumentation, logger, chainID, nodeID)
}

func NoOpTracer() Tracer {
	return &noOpTracer{}
}

type noOpTracer struct{}

func (n *noOpTracer) Write(_ Entry) {}
func (n *noOpTracer) ReadTable(_ string) ([]byte, error) {
	return nil, errors.New("no-op tracer does not support reading")
}
func (n *noOpTracer) IsCollecting(_ string) bool { return false }
func (n *noOpTracer) Stop()                      {}
