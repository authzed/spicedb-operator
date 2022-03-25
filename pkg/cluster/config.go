package cluster

type MigrationConfig struct {
	LogLevel              string
	DatastoreEngine       string
	DatastoreURI          string
	SpannerCredsSecretRef string
	TargetSpiceDBTag      string
}

type SpiceConfig struct {
	LogLevel     string
	Replicas     int32
	PresharedKey string
}

type Config struct {
	MigrationConfig
	SpiceConfig
}
