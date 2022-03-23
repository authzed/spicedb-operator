package cluster

type MigrationConfig struct {
	DatastoreEngine  string
	DatastoreURI     string
	SpannerCreds     string
	TargetSpiceDBTag string
}

type SpiceConfig struct {
}

type Config struct {
	MigrationConfig
	SpiceConfig
}
