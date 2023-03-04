package remote

import (
	"context"
	"fmt"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
	"github.com/tendermint/tendermint/libs/log"
)

const (
	NodeIDTag  = "node_id"
	ChainIDTag = "chain_id"
)

// EventCollectorConfig is the influxdb client configuration used for
// collecting events.
type EventCollectorConfig struct {
	// URL is the influxdb url.
	URL string `mapstructure:"url"`
	// Token is the influxdb token.
	Token string `mapstructure:"token"`
	// Org is the influxdb organization.
	Org string `mapstructure:"org"`
	// Bucket is the influxdb bucket.
	Bucket string `mapstructure:"bucket"`
	// BatchSize is the number of points to write in a single batch.
	BatchSize int `mapstructure:"batch_size"`
}

// DefaultEventCollectorConfig returns the default configuration.
func DefaultEventCollectorConfig() *EventCollectorConfig {
	return &EventCollectorConfig{
		URL:       "",
		Org:       "core/app",
		Bucket:    "e2e",
		BatchSize: 10,
	}
}

// Client is an influxdb client that can be used to push events to influxdb. It
// is used to collect trace data from many different nodes in a network. If
// there is no URL in the config.toml, then the underlying client is nil and no
// points will be written. The provided chainID and nodeID are used to tag all
// points. The underlying client is exposed to allow for custom writes, but the
// WritePoint method should be used for most cases, as it enforces the schema.
type Client struct {
	ctx    context.Context
	cancel context.CancelFunc
	*EventCollectorConfig

	// chainID is added as a tag all points
	chainID string

	// nodeID is added as a tag all points
	nodeID string

	// Client is the influxdb client. This field is nil if no connection is
	// established.
	Client influxdb2.Client
}

// Stop closes the influxdb client.
func (c *Client) Stop() {
	writeAPI := c.Client.WriteAPI(c.Org, c.Bucket)
	writeAPI.Flush()
	c.Client.Close()
	c.cancel()
}

// NewClient creates a new influxdb client using the provided config. If there
// is no URL configured, then the underlying client will be nil, and each
// attempt to write a point will do nothing. The provided chainID and nodeID are
// used to tag all points.
func NewClient(cfg *EventCollectorConfig, logger log.Logger, chainID, nodeID string) (*Client, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cli := &Client{
		EventCollectorConfig: cfg,
		Client:               nil,
		ctx:                  ctx,
		cancel:               cancel,
		chainID:              chainID,
		nodeID:               nodeID,
	}
	if cfg.URL == "" {
		return cli, nil
	}
	cli.Client = influxdb2.NewClientWithOptions(
		cfg.URL,
		cfg.Token,
		influxdb2.DefaultOptions().
			SetBatchSize(uint(cfg.BatchSize)),
	)
	ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	alive, err := cli.Client.Ping(ctx)
	if err != nil {
		logger.Error(err.Error())
		return nil, err
	}
	if !alive {
		logger.Error("influxdb is not alive")
		return nil, fmt.Errorf("failure to ping configured influxdb: %s", cfg.URL)
	}
	logger.Info("connected to influxdb", "url", cfg.URL)
	go cli.logErrors(logger)
	return cli, nil
}

// logErrors empties the writeAPI error channel and logs any errors.
func (c *Client) logErrors(logger log.Logger) {
	writeAPI := c.Client.WriteAPI(c.Org, c.Bucket)
	for {
		select {
		case err := <-writeAPI.Errors():
			logger.Error("event collector: influxdb write error", "err", err)
		case <-c.ctx.Done():
			return
		}
	}
}

// WritePoint async writes a point to influxdb. To enforce the schema, it
// automatically adds the chain_id and node_id tags, along with setting the
// timestamp to the current time. If the underlying client is nil, it does
// nothing. The "table" arg is used as the influxdb "measurement" for the point.
// If other tags are needed, use WriteCustomPoint.
func (c *Client) WritePoint(table string, fields map[string]interface{}) {
	if c.Client == nil {
		return
	}
	writeAPI := c.Client.WriteAPI(c.Org, c.Bucket)
	tags := map[string]string{
		NodeIDTag:  c.nodeID,
		ChainIDTag: c.chainID,
	}
	p := write.NewPoint(table, tags, fields, time.Now())
	writeAPI.WritePoint(p)
}
