package node

import (
	"time"

	"github.com/prometheus/client_golang/prometheus/push"
	cfg "github.com/tendermint/tendermint/config"
	cs "github.com/tendermint/tendermint/consensus"
	"github.com/tendermint/tendermint/libs/log"
	mempl "github.com/tendermint/tendermint/mempool"
	"github.com/tendermint/tendermint/p2p"
	sm "github.com/tendermint/tendermint/state"
)

var (
	PushGateWayURL = ""
	PushMetrics    = false
	PushInterval   = 5 * time.Second
)

type Pusher struct {
	*push.Pusher
	done chan struct{}
}

func MetricsPusher(cm *cs.Metrics, pm *p2p.Metrics, mm *mempl.Metrics, sm *sm.Metrics, config *cfg.InstrumentationConfig) *Pusher {
	if PushGateWayURL == "" || !config.Prometheus || !PushMetrics {
		return nil
	}

	p := push.New(PushGateWayURL, config.Namespace)
	p = cm.Push(p)
	p = pm.Push(p)
	p = mm.Push(p)
	p = sm.Push(p)

	return &Pusher{Pusher: p, done: make(chan struct{}, 1)}
}

func (p *Pusher) Start(logger log.Logger) {
	if p == nil {
		return
	}
	for {
		err := p.Add()
		if err != nil {
			logger.Error("failed to push metrics", "err", err)
		}
		select {
		case <-p.done:
			return
		case <-time.After(PushInterval):
		}
	}
}

func (p *Pusher) Stop() {
	if p == nil {
		return
	}
	p.done <- struct{}{}
}
