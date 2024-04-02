package trace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/libs/log"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
)

const (
	NodeIDTag  = "node_id"
	ChainIDTag = "chain_id"
)

// ClientConfigConfig is the influxdb client configuration used for
// collecting events.
type ClientConfigConfig struct {
	// URL is the influxdb url.
	URL string `mapstructure:"trace_push_url"`
	// Token is the influxdb token.
	Token string `mapstructure:"trace_auth_token"`
	// Org is the influxdb organization.
	Org string `mapstructure:"trace_org"`
	// Bucket is the influxdb bucket.
	Bucket string `mapstructure:"trace_db"`
	// BatchSize is the number of points to write in a single batch.
	BatchSize int `mapstructure:"trace_push_batch_size"`
}

var _ Tracer = (*InfluxClient)(nil)

// Client is an influxdb client that can be used to push events to influxdb. It
// is used to collect trace data from many different nodes in a network. If
// there is no URL in the config.toml, then the underlying client is nil and no
// points will be written. The provided chainID and nodeID are used to tag all
// points. The underlying client is exposed to allow for custom writes, but the
// WritePoint method should be used for most cases, as it enforces the schema.
type InfluxClient struct {
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

	// client is the influxdb client. This field is nil if no connection is
	// established.
	client influxdb2.Client

	// writeAPI is the write api for the client.
	writeAPI api.WriteAPI

	// Logger is the logger for the client.
	Logger log.Logger
}

// Stop closes the influxdb client.
func (c *InfluxClient) Stop() {
	c.cancel()
	if c.client == nil {
		return
	}
	writeAPI := c.client.WriteAPI(c.cfg.TraceOrg, c.cfg.TraceDB)
	writeAPI.Flush()
	c.client.Close()
}

// NewInfluxClient creates a new influxdb client using the provided config. If there
// is no URL configured, then the underlying client will be nil, and each
// attempt to write a point will do nothing. The provided chainID and nodeID are
// used to tag all points.
func NewInfluxClient(cfg *config.InstrumentationConfig, logger log.Logger, chainID, nodeID string) (*InfluxClient, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cli := &InfluxClient{
		cfg:     cfg,
		client:  nil,
		ctx:     ctx,
		cancel:  cancel,
		chainID: chainID,
		nodeID:  nodeID,
		tables:  stringToMap(cfg.TracingTables),
		Logger:  logger,
	}
	if cfg.TracePushURL == "" {
		return cli, nil
	}
	cli.client = influxdb2.NewClientWithOptions(
		cfg.TracePushURL,
		cfg.TraceAuthToken,
		influxdb2.DefaultOptions().
			SetBatchSize(uint(cfg.TraceBufferSize)),
	)
	ctx, cancel = context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	alive, err := cli.client.Ping(ctx)
	if err != nil {
		return nil, err
	}
	if !alive {
		return nil, fmt.Errorf("failure to ping configured influxdb: %s", cfg.TracePushURL)
	}
	logger.Info("connected to influxdb", "url", cfg.TracePushURL)
	go cli.logErrors(logger)
	cli.writeAPI = cli.client.WriteAPI(cfg.TraceOrg, cfg.TraceDB)
	return cli, nil
}

// logErrors empties the writeAPI error channel and logs any errors.
func (c *InfluxClient) logErrors(logger log.Logger) {
	writeAPI := c.client.WriteAPI(c.cfg.TraceOrg, c.cfg.TraceDB)
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
func (c *InfluxClient) IsCollecting(table string) bool {
	if c.client == nil {
		return false
	}
	_, has := c.tables[table]
	return has
}

// Write async writes a point to influxdb. To enforce the schema, it
// automatically adds the chain_id and node_id tags, along with setting the
// timestamp to the current time. If the underlying client is nil, it does
// nothing. The "table" arg is used as the influxdb "measurement" for the point.
// If other tags are needed, use WriteCustomPoint.
func (c *InfluxClient) Write(e Entry) {
	table := e.Table()
	if !c.IsCollecting(table) {
		return
	}

	tags := map[string]string{
		NodeIDTag:  c.nodeID,
		ChainIDTag: c.chainID,
	}
	ir, err := e.InfluxRepr()
	if err != nil {
		c.Logger.Error("failed to convert event to influx representation", "err", err)
		return
	}
	p := write.NewPoint(table, tags, ir, time.Now())
	c.writeAPI.WritePoint(p)
}

func (c *InfluxClient) ReadTable(string) ([]byte, error) {
	return nil, errors.New("reading not supported using the InfluxDB tracing client")
}

func stringToMap(tables string) map[string]struct{} {
	parsedTables := splitAndTrimEmpty(tables, ",", " ")
	m := make(map[string]struct{})
	for _, s := range parsedTables {
		m[s] = struct{}{}
	}
	return m
}

// splitAndTrimEmpty slices s into all subslices separated by sep and returns a
// slice of the string s with all leading and trailing Unicode code points
// contained in cutset removed. If sep is empty, SplitAndTrim splits after each
// UTF-8 sequence. First part is equivalent to strings.SplitN with a count of
// -1.  also filter out empty strings, only return non-empty strings.
//
// NOTE: this is copy pasted from the config package to avoid a circular
// dependency. See the function of the same name for tests.
func splitAndTrimEmpty(s, sep, cutset string) []string {
	if s == "" {
		return []string{}
	}

	spl := strings.Split(s, sep)
	nonEmptyStrings := make([]string, 0, len(spl))
	for i := 0; i < len(spl); i++ {
		element := strings.Trim(spl[i], cutset)
		if element != "" {
			nonEmptyStrings = append(nonEmptyStrings, element)
		}
	}
	return nonEmptyStrings
}
