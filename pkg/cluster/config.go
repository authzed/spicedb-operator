package cluster

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fatih/camelcase"
	"github.com/jzelinskie/stringz"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
)

type Config struct {
	MigrationConfig
	SpiceConfig
}

// NewConfig checks that the values in the config + the secret are sane
func NewConfig(image string, config map[string]string, secret *corev1.Secret) (*Config, error) {
	passthroughConfig := make(map[string]string, 0)

	datastoreEngine, ok := config["datastoreEngine"]
	if !ok {
		return nil, fmt.Errorf("datastoreEngine is a required field")
	}
	passthroughConfig["datastoreEngine"] = config["datastoreEngine"]
	delete(config, "datastoreEngine")

	datastoreURI, ok := secret.Data["datastore_uri"]
	if !ok {
		return nil, fmt.Errorf("secret must contain a datastore-uri field")
	}

	psk, ok := secret.Data["preshared_key"]
	if !ok {
		return nil, fmt.Errorf("secret must contain a preshared_key field")
	}

	replicas, err := strconv.ParseInt(stringz.DefaultEmpty(config["replicas"], "2"), 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid value for replicas %q: %w", replicas, err)
	}
	delete(config, "replicas")

	envPrefix := stringz.DefaultEmpty(config["envPrefix"], "SPICEDB_")
	delete(config, "envPrefix")
	spicedbCmd := stringz.DefaultEmpty(config["cmd"], "spicedb")
	delete(config, "cmd")

	// generate secret refs for tls if specified
	if len(config["tlsSecretName"]) > 0 {
		const (
			TLSKey = "/tls/tls.key"
			TLSCrt = "/tls/tls.crt"
		)
		passthroughDefault := func(key string, fallback string) {
			passthroughConfig[key] = stringz.DefaultEmpty(config[key], fallback)
		}
		// set to the configured TLS secret unless explicitly set in config
		passthroughDefault("grpcTLSKeyPath", TLSKey)
		passthroughDefault("grpcTLSCertPath", TLSCrt)
		passthroughDefault("dispatchClusterTLSKeyPath", TLSKey)
		passthroughDefault("dispatchClusterTLSCertPath", TLSCrt)
		passthroughDefault("httpTLSKeyPath", TLSKey)
		passthroughDefault("httpTLSCertPath", TLSCrt)
		passthroughDefault("dashboardTLSKeyPath", TLSKey)
		passthroughDefault("dashboardTLSCertPath", TLSCrt)
		passthroughDefault("metricsTLSKeyPath", TLSKey)
		passthroughDefault("metricsTLSCertPath", TLSCrt)
		passthroughDefault("dispatchUpstreamCAPath", TLSCrt)
	}

	// the rest of the config is passed through to spicedb
	for k := range config {
		passthroughConfig[k] = config[k]
	}

	// strip sensitive values from passthrough config (if they have been
	// inadvertently set by a user)
	for k := range passthroughConfig {
		if strings.EqualFold(k, "datastoreConnUri") {
			delete(passthroughConfig, k)
		}
		if strings.EqualFold(k, "grpcPresharedKey") {
			delete(passthroughConfig, k)
		}
		if strings.EqualFold(k, "presharedKey") {
			delete(passthroughConfig, k)
		}
		if strings.EqualFold(k, "preshared_key") {
			delete(passthroughConfig, k)
		}
		if strings.EqualFold(k, "datastore_uri") {
			delete(passthroughConfig, k)
		}
	}

	// Note: The config objects hold values from the passed secret for
	// hashing purposes; these should not be used directly (instead the secret
	// should be mounted)
	return &Config{MigrationConfig: MigrationConfig{
		DatastoreEngine:        datastoreEngine,
		DatastoreURI:           string(datastoreURI),
		SpannerCredsSecretRef:  config["spannerCredentials"],
		TargetSpiceDBImage:     image,
		EnvPrefix:              envPrefix,
		SpiceDBCmd:             spicedbCmd,
		DatastoreTLSSecretName: config["datastoreTLSSecretName"],
	}, SpiceConfig: SpiceConfig{
		PresharedKey:  string(psk),
		Replicas:      int32(replicas),
		EnvPrefix:     envPrefix,
		SpiceDBCmd:    spicedbCmd,
		TLSSecretName: config["tlsSecretName"],
		SecretName:    secret.GetName(),
		Passthrough:   passthroughConfig,
	}}, nil
}

// MigrationConfig stores data that is relevant for running migrations
// or deciding if migrations need to be run
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

// SpiceConfig contains config relevant to running spicedb or determining
// if spicedb needs to be updated
type SpiceConfig struct {
	Replicas      int32
	PresharedKey  string
	EnvPrefix     string
	SpiceDBCmd    string
	TLSSecretName string
	SecretName    string
	Passthrough   map[string]string
}

// ToEnvVarApplyConfiguration returns a set of env variables to apply to a
// spicedb container
func (c *SpiceConfig) ToEnvVarApplyConfiguration(nn types.NamespacedName) []*applycorev1.EnvVarApplyConfiguration {
	// Add non-passthrough config that is either generated directly by the
	// controller (dispatch address), has some direct effect on the cluster
	// (tls), or lives in an external secret (preshared key).
	envVars := []*applycorev1.EnvVarApplyConfiguration{
		applycorev1.EnvVar().WithName(c.EnvPrefix + "_DISPATCH_UPSTREAM_ADDR").
			WithValue(fmt.Sprintf("kubernetes:///%s.%s:dispatch", nn.Name, nn.Namespace)),
		applycorev1.EnvVar().WithName(c.EnvPrefix + "_DATASTORE_CONN_URI").
			WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(
				applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("datastore_uri"))),
		applycorev1.EnvVar().WithName(c.EnvPrefix + "_GRPC_PRESHARED_KEY").
			WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(
				applycorev1.SecretKeySelector().WithName(c.SecretName).WithKey("preshared_key"))),
	}

	// Passthrough config is user-provided and only affects spicedb runtime.
	for k, v := range c.Passthrough {
		envVars = append(envVars, applycorev1.EnvVar().
			WithName(ToEnvVarName(c.EnvPrefix, k)).WithValue(v))
	}

	return envVars
}

// ToEnvVarName converts a key from the api object into an env var name.
// the key isCamelCased will be converted to PREFIX_IS_CAMEL_CASED
func ToEnvVarName(prefix string, key string) string {
	envVarParts := []string{strings.ToUpper(prefix)}
	for _, p := range camelcase.Split(key) {
		envVarParts = append(envVarParts, strings.ToUpper(p))
	}
	return strings.Join(envVarParts, "_")
}
