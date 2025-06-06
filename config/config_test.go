package config_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cometbft/cometbft/config"
)

func TestDefaultConfig(t *testing.T) {
	assert := assert.New(t)

	// set up some defaults
	cfg := config.DefaultConfig()
	assert.NotNil(cfg.P2P)
	assert.NotNil(cfg.Mempool)
	assert.NotNil(cfg.Consensus)

	// check the root dir stuff...
	cfg.SetRoot("/foo")
	cfg.Genesis = "bar"
	cfg.DBPath = "/opt/data"
	cfg.BlockstorePath = "/opt/blockstore"
	cfg.Mempool.WalPath = "wal/mem/"

	assert.Equal("/foo/bar", cfg.GenesisFile())
	assert.Equal("/opt/data", cfg.DBDir())
	assert.Equal("/opt/blockstore", cfg.BlockstoreDir())
	assert.Equal("/foo/wal/mem", cfg.Mempool.WalDir())
}

func TestConfigValidateBasic(t *testing.T) {
	cfg := config.DefaultConfig()
	assert.NoError(t, cfg.ValidateBasic())

	// tamper with timeout_propose
	cfg.Consensus.TimeoutPropose = -10 * time.Second
	assert.Error(t, cfg.ValidateBasic())
	cfg.Consensus.TimeoutPropose = 3 * time.Second

	cfg.Consensus.CreateEmptyBlocks = false
	cfg.Mempool.Type = config.MempoolTypeNop
	assert.Error(t, cfg.ValidateBasic())
}

func TestTLSConfiguration(t *testing.T) {
	assert := assert.New(t)
	cfg := config.DefaultConfig()
	cfg.SetRoot("/home/user")

	cfg.RPC.TLSCertFile = "file.crt"
	assert.Equal("/home/user/config/file.crt", cfg.RPC.CertFile())
	cfg.RPC.TLSKeyFile = "file.key"
	assert.Equal("/home/user/config/file.key", cfg.RPC.KeyFile())

	cfg.RPC.TLSCertFile = "/abs/path/to/file.crt"
	assert.Equal("/abs/path/to/file.crt", cfg.RPC.CertFile())
	cfg.RPC.TLSKeyFile = "/abs/path/to/file.key"
	assert.Equal("/abs/path/to/file.key", cfg.RPC.KeyFile())
}

func TestBaseConfigValidateBasic(t *testing.T) {
	cfg := config.TestBaseConfig()
	assert.NoError(t, cfg.ValidateBasic())

	// tamper with log format
	cfg.LogFormat = "invalid"
	assert.Error(t, cfg.ValidateBasic())
}

func TestRPCConfigValidateBasic(t *testing.T) {
	cfg := config.TestRPCConfig()
	assert.NoError(t, cfg.ValidateBasic())

	fieldsToTest := []string{
		"GRPCMaxOpenConnections",
		"MaxOpenConnections",
		"MaxSubscriptionClients",
		"MaxSubscriptionsPerClient",
		"TimeoutBroadcastTxCommit",
		"MaxBodyBytes",
		"MaxHeaderBytes",
		"MaxRequestBatchSize",
	}

	for _, fieldName := range fieldsToTest {
		reflect.ValueOf(cfg).Elem().FieldByName(fieldName).SetInt(-1)
		assert.Error(t, cfg.ValidateBasic())
		reflect.ValueOf(cfg).Elem().FieldByName(fieldName).SetInt(0)
	}
}

func TestP2PConfigValidateBasic(t *testing.T) {
	cfg := config.TestP2PConfig()
	assert.NoError(t, cfg.ValidateBasic())

	fieldsToTest := []string{
		"MaxNumInboundPeers",
		"MaxNumOutboundPeers",
		"FlushThrottleTimeout",
		"MaxPacketMsgPayloadSize",
		"SendRate",
		"RecvRate",
	}

	for _, fieldName := range fieldsToTest {
		reflect.ValueOf(cfg).Elem().FieldByName(fieldName).SetInt(-1)
		assert.Error(t, cfg.ValidateBasic())
		reflect.ValueOf(cfg).Elem().FieldByName(fieldName).SetInt(0)
	}
}

func TestMempoolConfigValidateBasic(t *testing.T) {
	cfg := config.TestMempoolConfig()
	assert.NoError(t, cfg.ValidateBasic())

	fieldsToTest := []string{
		"Size",
		"MaxTxsBytes",
		"CacheSize",
		"MaxTxBytes",
	}

	for _, fieldName := range fieldsToTest {
		reflect.ValueOf(cfg).Elem().FieldByName(fieldName).SetInt(-1)
		assert.Error(t, cfg.ValidateBasic())
		reflect.ValueOf(cfg).Elem().FieldByName(fieldName).SetInt(0)
	}

	reflect.ValueOf(cfg).Elem().FieldByName("Type").SetString("invalid")
	assert.Error(t, cfg.ValidateBasic())
}

func TestStateSyncConfigValidateBasic(t *testing.T) {
	cfg := config.TestStateSyncConfig()
	require.NoError(t, cfg.ValidateBasic())
}

func TestBlockSyncConfigValidateBasic(t *testing.T) {
	cfg := config.TestBlockSyncConfig()
	assert.NoError(t, cfg.ValidateBasic())

	// tamper with version
	cfg.Version = "v1"
	assert.Error(t, cfg.ValidateBasic())

	cfg.Version = "invalid"
	assert.Error(t, cfg.ValidateBasic())
}

func TestConsensusConfig_ValidateBasic(t *testing.T) {
	//nolint: lll
	testcases := map[string]struct {
		modify    func(*config.ConsensusConfig)
		expectErr bool
	}{
		"TimeoutPropose":                       {func(c *config.ConsensusConfig) { c.TimeoutPropose = time.Second }, false},
		"TimeoutPropose negative":              {func(c *config.ConsensusConfig) { c.TimeoutPropose = -1 }, true},
		"TimeoutProposeDelta":                  {func(c *config.ConsensusConfig) { c.TimeoutProposeDelta = time.Second }, false},
		"TimeoutProposeDelta negative":         {func(c *config.ConsensusConfig) { c.TimeoutProposeDelta = -1 }, true},
		"TimeoutPrevote":                       {func(c *config.ConsensusConfig) { c.TimeoutPrevote = time.Second }, false},
		"TimeoutPrevote negative":              {func(c *config.ConsensusConfig) { c.TimeoutPrevote = -1 }, true},
		"TimeoutPrevoteDelta":                  {func(c *config.ConsensusConfig) { c.TimeoutPrevoteDelta = time.Second }, false},
		"TimeoutPrevoteDelta negative":         {func(c *config.ConsensusConfig) { c.TimeoutPrevoteDelta = -1 }, true},
		"TimeoutPrecommit":                     {func(c *config.ConsensusConfig) { c.TimeoutPrecommit = time.Second }, false},
		"TimeoutPrecommit negative":            {func(c *config.ConsensusConfig) { c.TimeoutPrecommit = -1 }, true},
		"TimeoutPrecommitDelta":                {func(c *config.ConsensusConfig) { c.TimeoutPrecommitDelta = time.Second }, false},
		"TimeoutPrecommitDelta negative":       {func(c *config.ConsensusConfig) { c.TimeoutPrecommitDelta = -1 }, true},
		"TimeoutCommit":                        {func(c *config.ConsensusConfig) { c.TimeoutCommit = time.Second }, false},
		"TimeoutCommit negative":               {func(c *config.ConsensusConfig) { c.TimeoutCommit = -1 }, true},
		"PeerGossipSleepDuration":              {func(c *config.ConsensusConfig) { c.PeerGossipSleepDuration = time.Second }, false},
		"PeerGossipSleepDuration negative":     {func(c *config.ConsensusConfig) { c.PeerGossipSleepDuration = -1 }, true},
		"PeerQueryMaj23SleepDuration":          {func(c *config.ConsensusConfig) { c.PeerQueryMaj23SleepDuration = time.Second }, false},
		"PeerQueryMaj23SleepDuration negative": {func(c *config.ConsensusConfig) { c.PeerQueryMaj23SleepDuration = -1 }, true},
		"DoubleSignCheckHeight negative":       {func(c *config.ConsensusConfig) { c.DoubleSignCheckHeight = -1 }, true},
	}
	for desc, tc := range testcases {
		tc := tc // appease linter
		t.Run(desc, func(t *testing.T) {
			cfg := config.DefaultConsensusConfig()
			tc.modify(cfg)

			err := cfg.ValidateBasic()
			if tc.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestInstrumentationConfigValidateBasic(t *testing.T) {
	cfg := config.TestInstrumentationConfig()
	assert.NoError(t, cfg.ValidateBasic())

	// tamper with maximum open connections
	cfg.MaxOpenConnections = -1
	assert.Error(t, cfg.ValidateBasic())
}

func TestProposeWithCustomTimeout(t *testing.T) {
	cfg := config.DefaultConsensusConfig()

	//  customTimeout is 0, should fallback to default timeout
	round := int32(1)
	expectedTimeout := time.Duration(cfg.TimeoutPropose.Nanoseconds()+cfg.TimeoutProposeDelta.Nanoseconds()*int64(round)) * time.Nanosecond
	assert.Equal(t, expectedTimeout, cfg.ProposeWithCustomTimeout(round, time.Duration(0)))

	// customTimeout is not 0
	customTimeout := 2 * time.Second
	expectedTimeout = time.Duration(customTimeout.Nanoseconds()+cfg.TimeoutProposeDelta.Nanoseconds()*int64(round)) * time.Nanosecond
	assert.Equal(t, expectedTimeout, cfg.ProposeWithCustomTimeout(round, customTimeout))
}

func TestCommitWithCustomTimeout(t *testing.T) {
	cfg := config.DefaultConsensusConfig()

	// customTimeout is 0, should fallback to default timeout
	inputTime := time.Now()
	expectedTime := inputTime.Add(cfg.TimeoutCommit)
	assert.Equal(t, expectedTime, cfg.CommitWithCustomTimeout(inputTime, time.Duration(0)))

	// customTimeout is not 0
	customTimeout := 2 * time.Second
	expectedTime = inputTime.Add(customTimeout)
	assert.Equal(t, expectedTime, cfg.CommitWithCustomTimeout(inputTime, customTimeout))
}

func TestBlockstoreDir(t *testing.T) {
	cfg := config.DefaultConfig()

	// Test 1: When BlockstorePath is not set, it should default to DBDir
	cfg.SetRoot("/foo")
	cfg.DBPath = "data"
	cfg.BlockstorePath = ""
	assert.Equal(t, cfg.DBDir(), cfg.BlockstoreDir())

	// Test 2: When BlockstorePath is set, it should use that path
	cfg.BlockstorePath = "blockstore"
	assert.Equal(t, "/foo/blockstore", cfg.BlockstoreDir())

	// Test 3: Test with absolute paths
	cfg.BlockstorePath = "/opt/blockstore"
	assert.Equal(t, "/opt/blockstore", cfg.BlockstoreDir())
}
