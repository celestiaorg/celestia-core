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

func WriteQueueSize(client trace.Tracer, name string, size int) {
	client.Write(QueueSize{Name: name, Size: size})
}
