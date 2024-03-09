package p2p

import (
	"fmt"
	"reflect"
	"regexp"
	"sync"

	"github.com/go-kit/kit/metrics"
	"github.com/go-kit/kit/metrics/discard"
	"github.com/go-kit/kit/metrics/prometheus"
	stdprometheus "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

const (
	// MetricsSubsystem is a subsystem shared by all metrics exposed by this
	// package.
	MetricsSubsystem = "p2p"
)

var (
	// valueToLabelRegexp is used to find the golang package name and type name
	// so that the name can be turned into a prometheus label where the characters
	// in the label do not include prometheus special characters such as '*' and '.'.
	valueToLabelRegexp = regexp.MustCompile(`\*?(\w+)\.(.*)`)
)

// Metrics contains metrics exposed by this package.
type Metrics struct {
	// Number of peers.
	Peers metrics.PGauge
	// Number of bytes received from a given peer.
	PeerReceiveBytesTotal metrics.PCounter
	// Number of bytes sent to a given peer.
	PeerSendBytesTotal metrics.PCounter
	// Pending bytes to be sent to a given peer.
	PeerPendingSendBytes metrics.PGauge
	// Number of transactions submitted by each peer.
	NumTxs metrics.PGauge
	// Number of bytes of each message type received.
	MessageReceiveBytesTotal metrics.PCounter
	// Number of bytes of each message type sent.
	MessageSendBytesTotal metrics.PCounter
}

func (m *Metrics) Push(p *push.Pusher) *push.Pusher {
	p = p.Collector(m.Peers.Collector())
	p = p.Collector(m.PeerReceiveBytesTotal.Collector())
	p = p.Collector(m.PeerSendBytesTotal.Collector())
	p = p.Collector(m.PeerPendingSendBytes.Collector())
	p = p.Collector(m.NumTxs.Collector())
	p = p.Collector(m.MessageReceiveBytesTotal.Collector())
	p = p.Collector(m.MessageSendBytesTotal.Collector())
	return p
}

// PrometheusMetrics returns Metrics build using Prometheus client library.
// Optionally, labels can be provided along with their values ("foo",
// "fooValue").
func PrometheusMetrics(namespace string, labelsAndValues ...string) *Metrics {
	labels := []string{}
	for i := 0; i < len(labelsAndValues); i += 2 {
		labels = append(labels, labelsAndValues[i])
	}
	return &Metrics{
		Peers: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "peers",
			Help:      "Number of peers.",
		}, labels).WithP(labelsAndValues...),
		PeerReceiveBytesTotal: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "peer_receive_bytes_total",
			Help:      "Number of bytes received from a given peer.",
		}, append(labels, "peer_id", "chID")).WithP(labelsAndValues...),
		PeerSendBytesTotal: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "peer_send_bytes_total",
			Help:      "Number of bytes sent to a given peer.",
		}, append(labels, "peer_id", "chID")).WithP(labelsAndValues...),
		PeerPendingSendBytes: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "peer_pending_send_bytes",
			Help:      "Pending bytes to be sent to a given peer.",
		}, append(labels, "peer_id")).WithP(labelsAndValues...),
		NumTxs: prometheus.NewGaugeFrom(stdprometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "num_txs",
			Help:      "Number of transactions submitted by each peer.",
		}, append(labels, "peer_id")).WithP(labelsAndValues...),
		MessageReceiveBytesTotal: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "message_receive_bytes_total",
			Help:      "Number of bytes of each message type received.",
		}, append(labels, "message_type", "chID", "peer_id")).WithP(labelsAndValues...),
		MessageSendBytesTotal: prometheus.NewCounterFrom(stdprometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: MetricsSubsystem,
			Name:      "message_send_bytes_total",
			Help:      "Number of bytes of each message type sent.",
		}, append(labels, "message_type", "chID", "peer_id")).WithP(labelsAndValues...),
	}
}

func NopMetrics() *Metrics {
	return &Metrics{
		Peers:                    discard.NewPGauge(),
		PeerReceiveBytesTotal:    discard.NewPCounter(),
		PeerSendBytesTotal:       discard.NewPCounter(),
		PeerPendingSendBytes:     discard.NewPGauge(),
		NumTxs:                   discard.NewPGauge(),
		MessageReceiveBytesTotal: discard.NewPCounter(),
		MessageSendBytesTotal:    discard.NewPCounter(),
	}
}

type metricsLabelCache struct {
	mtx               *sync.RWMutex
	messageLabelNames map[reflect.Type]string
}

// ValueToMetricLabel is a method that is used to produce a prometheus label value of the golang
// type that is passed in.
// This method uses a map on the Metrics struct so that each label name only needs
// to be produced once to prevent expensive string operations.
func (m *metricsLabelCache) ValueToMetricLabel(i interface{}) string {
	t := reflect.TypeOf(i)
	m.mtx.RLock()

	if s, ok := m.messageLabelNames[t]; ok {
		m.mtx.RUnlock()
		return s
	}
	m.mtx.RUnlock()

	s := t.String()
	ss := valueToLabelRegexp.FindStringSubmatch(s)
	l := fmt.Sprintf("%s_%s", ss[1], ss[2])
	m.mtx.Lock()
	defer m.mtx.Unlock()
	m.messageLabelNames[t] = l
	return l
}

func newMetricsLabelCache() *metricsLabelCache {
	return &metricsLabelCache{
		mtx:               &sync.RWMutex{},
		messageLabelNames: map[reflect.Type]string{},
	}
}
