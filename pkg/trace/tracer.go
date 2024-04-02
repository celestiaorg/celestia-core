package trace

import (
	"errors"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
)

type Tracer interface {
	Write(table string, data map[string]interface{})
	ReadTable(table string) ([]byte, error)
	IsCollecting(table string) bool
	Stop()
}

func NewTracer(cfg *config.InstrumentationConfig, logger log.Logger, chainID, nodeID string) (Tracer, error) {
	return NewInfluxClient(cfg, logger, chainID, nodeID)
}

func NoOpTracer() Tracer {
	return &noOpTracer{}
}

type noOpTracer struct{}

func (n *noOpTracer) Write(_ string, _ map[string]interface{}) {}
func (n *noOpTracer) ReadTable(_ string) ([]byte, error) {
	return nil, errors.New("no-op tracer does not support reading")
}
func (n *noOpTracer) IsCollecting(_ string) bool { return false }
func (n *noOpTracer) Stop()                      {}
