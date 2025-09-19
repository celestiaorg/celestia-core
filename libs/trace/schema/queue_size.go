package schema

import (
	"github.com/cometbft/cometbft/libs/trace"
)

func QueueSizeTables() []string {
	return []string{
		QueueSizeTable,
	}
}

const (
	QueueSizeTable = "queue_size"
)

type QueueSize struct {
	Name string `json:"name"`
	Size int    `json:"size"`
}

func (QueueSize) Table() string {
	return QueueSizeTable
}

var TraceClient trace.Tracer

func WriteQueueSize(name string, size int) {
	TraceClient.Write(QueueSize{Name: name, Size: size})
}
