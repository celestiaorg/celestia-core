package trace

const (
	FlagInfluxDBURL              = "influxdb-url"
	FlagInfluxDBToken            = "influxdb-token"
	FlagInfluxDBURLDescription   = "URL of the InfluxDB instance to use for arbitrary data collection. If not specified, data will not be collected"
	FlagInfluxDBTokenDescription = "Token to use when writing to the InfluxDB instance. Must be specified if 'influxdb-url' is specified"
)
