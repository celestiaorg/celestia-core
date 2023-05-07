package trace

const (
	FlagInfluxDBURL              = "influxdb-url"
	FlagInfluxDBToken            = "influxdb-token"
	FlagInfluxDBURLDescription   = "URL of the InfluxDB instance to use for arbitrary data collection. If not specified, data will not be collected"
	FlagInfluxDBTokenDescription = "Token to use when writing to the InfluxDB instance. Must be specified if 'influxdb-url' is specified"

	FlagPyroscopeURL              = "pyroscope-url"
	FlagPyroscopeURLDescription   = "URL of the Pyroscop instance to use for continous profiling. If not specified, profiling will not be enabled"
	FlagPyroscopeTrace            = "pyroscope-trace"
	FlagPyroscopeTraceDescription = "enable adding trace data to pyroscope profiling"
)
