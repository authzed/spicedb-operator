package cluster

type MigrationConfig struct {
	LogLevel               string
	DatastoreEngine        string
	DatastoreURI           string
	SpannerCredsSecretRef  string
	TargetSpiceDBImage     string
	EnvPrefix              string
	SpiceDBCmd             string
	DatastoreTLSSecretName string
}

type SpiceConfig struct {
	LogLevel         string
	Replicas         int32
	PresharedKey     string
	EnvPrefix        string
	SpiceDBCmd       string
	TLSSecretName    string
	PrefixesRequired bool
	OverlapStrategy  string
}

type Config struct {
	MigrationConfig
	SpiceConfig
}
