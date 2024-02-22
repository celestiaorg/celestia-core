package trace

import (
	"context"
	"fmt"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api/write"

	"github.com/tendermint/tendermint/config"
	"github.com/tendermint/tendermint/libs/log"
)

const (
	NodeIDTag  = "node_id"
	ChainIDTag = "chain_id"
)

// ClientConfigConfig is the influxdb client configuration used for
// collecting events.
type ClientConfigConfig struct {
	// URL is the influxdb url.
	URL string `mapstructure:"influx_url"`
	// Token is the influxdb token.
	Token string `mapstructure:"influx_token"`
	// Org is the influxdb organization.
	Org string `mapstructure:"influx_org"`
	// Bucket is the influxdb bucket.
	Bucket string `mapstructure:"influx_bucket"`
	// BatchSize is the number of points to write in a single batch.
	BatchSize int `mapstructure:"influx_batch_size"`
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
	cfg    *config.InstrumentationConfig

	// chainID is added as a tag all points
	chainID string

	// nodeID is added as a tag all points
	nodeID string

	// tables is a map from table name to the schema of that table that are
	// configured to be collected.
	tables map[string]struct{}

	// Client is the influxdb client. This field is nil if no connection is
	// established.
	Client influxdb2.Client
}

// Stop closes the influxdb client.
func (c *Client) Stop() {
	c.cancel()
	if c.Client == nil {
		return
	}
	writeAPI := c.Client.WriteAPI(c.cfg.InfluxOrg, c.cfg.InfluxBucket)
	writeAPI.Flush()
	c.Client.Close()
}

// NewClient creates a new influxdb client using the provided config. If there
// is no URL configured, then the underlying client will be nil, and each
// attempt to write a point will do nothing. The provided chainID and nodeID are
// used to tag all points.
func NewClient(cfg *config.InstrumentationConfig, logger log.Logger, chainID, nodeID string) (*Client, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cli := &Client{
		cfg:     cfg,
		Client:  nil,
		ctx:     ctx,
		cancel:  cancel,
		chainID: chainID,
		nodeID:  nodeID,
		tables:  sliceToMap(cfg.InfluxTables),
	}
	if cfg.InfluxURL == "" {
		return cli, nil
	}
	cli.Client = influxdb2.NewClientWithOptions(
		cfg.InfluxURL,
		cfg.InfluxToken,
		influxdb2.DefaultOptions().
			SetBatchSize(uint(cfg.InfluxBatchSize)),
	)
	ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	alive, err := cli.Client.Ping(ctx)
	if err != nil {
		return nil, err
	}
	if !alive {
		return nil, fmt.Errorf("failure to ping configured influxdb: %s", cfg.InfluxURL)
	}
	logger.Info("connected to influxdb", "url", cfg.InfluxURL)
	go cli.logErrors(logger)
	return cli, nil
}

// logErrors empties the writeAPI error channel and logs any errors.
func (c *Client) logErrors(logger log.Logger) {
	writeAPI := c.Client.WriteAPI(c.cfg.InfluxOrg, c.cfg.InfluxBucket)
	for {
		select {
		case err := <-writeAPI.Errors():
			logger.Error("event collector: influxdb write error", "err", err)
		case <-c.ctx.Done():
			return
		}
	}
}

// IsCollecting returns true if the client is collecting events.
func (c *Client) IsCollecting(table string) bool {
	if c.Client == nil {
		return false
	}
	_, has := c.tables[table]
	return has
}

// WritePoint async writes a point to influxdb. To enforce the schema, it
// automatically adds the chain_id and node_id tags, along with setting the
// timestamp to the current time. If the underlying client is nil, it does
// nothing. The "table" arg is used as the influxdb "measurement" for the point.
// If other tags are needed, use WriteCustomPoint.
func (c *Client) WritePoint(table string, fields map[string]interface{}) {
	if !c.IsCollecting(table) {
		return
	}
	writeAPI := c.Client.WriteAPI(c.cfg.InfluxOrg, c.cfg.InfluxBucket)
	tags := map[string]string{
		NodeIDTag:  c.nodeID,
		ChainIDTag: c.chainID,
	}
	p := write.NewPoint(table, tags, fields, time.Now())
	writeAPI.WritePoint(p)
}

func sliceToMap(tables []string) map[string]struct{} {
	m := make(map[string]struct{})
	for _, s := range tables {
		m[s] = struct{}{}
	}
	return m
}
