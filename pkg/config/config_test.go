package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	v1 "k8s.io/client-go/applyconfigurations/core/v1"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/updates"
)

func TestToEnvVarName(t *testing.T) {
	tests := []struct {
		prefix string
		key    string
		want   string
	}{
		{"prefix", "key", "PREFIX_KEY"},
		{"prefix_", "key", "PREFIX_KEY"},
		{"SPICEDB", "grpcTLSKeyPath", "SPICEDB_GRPC_TLS_KEY_PATH"},
		{"SPICEDB", "grpcTlsKeyPath", "SPICEDB_GRPC_TLS_KEY_PATH"},
		{"SPICEDB", "dispatchUpstreamCAPath", "SPICEDB_DISPATCH_UPSTREAM_CA_PATH"},
	}
	for _, tt := range tests {
		t.Run(tt.prefix+"/"+tt.key, func(t *testing.T) {
			if got := toEnvVarName(tt.prefix, tt.key); got != tt.want {
				t.Errorf("toEnvVarName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewConfig(t *testing.T) {
	type args struct {
		cluster      v1alpha1.ClusterSpec
		status       v1alpha1.ClusterStatus
		globalConfig OperatorConfig
		secret       *corev1.Secret
	}
	tests := []struct {
		name          string
		args          args
		want          *Config
		wantEnvs      []string
		wantWarnings  []error
		wantErrs      []error
		wantPortCount int
	}{
		{
			name: "missing required",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
							{
								"test": "field"
							}
`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
				},
			},
			wantErrs: []error{
				fmt.Errorf("datastoreEngine is a required field"),
				fmt.Errorf("couldn't find channel for datastore \"\": %w", fmt.Errorf("no channel found for datastore \"\"")),
				fmt.Errorf("no update found in channel"),
				fmt.Errorf("secret must be provided"),
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
		},
		{
			name: "simple",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb"
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image:v1",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "info",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "memory",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "memory"
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "memory",
								Metadata: map[string]string{"datastore": "memory", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "memory",
					DatastoreURI:       "",
					TargetSpiceDBImage: "image:v1",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "memory",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "info",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           1,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "memory",
						"dispatchClusterEnabled": "false",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_ENGINE=memory",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=false",
			},
			wantPortCount: 3,
		},
		{
			name: "set image with tag explicitly",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"image": "adifferentimage:tag"
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "adifferentimage:tag",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "info",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "set image with digest explicitly",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"image": "adifferentimage@sha256:abc"
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "adifferentimage@sha256:abc",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "info",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "set replicas as int",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"replicas": 3
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image:v1",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "info",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           3,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "set replicas as string",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"replicas": "3"
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image:v1",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "info",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           3,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "set extra labels as string",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"extraPodLabels": "test=label,other=label"
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image:v1",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:       "info",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
					ExtraPodLabels: map[string]string{
						"test":  "label",
						"other": "label",
					},
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "set extra labels as map",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"extraPodLabels": {
							"test": "label",
							"other": "label"
						}
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image:v1",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:       "info",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
					ExtraPodLabels: map[string]string{
						"test":  "label",
						"other": "label",
					},
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "skip migrations bool",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"skipMigrations": true	
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:      "debug",
					DatastoreEngine:        "cockroachdb",
					DatastoreURI:           "uri",
					SpannerCredsSecretRef:  "",
					TargetSpiceDBImage:     "image:v1",
					EnvPrefix:              "SPICEDB",
					SpiceDBCmd:             "spicedb",
					DatastoreTLSSecretName: "",
					TargetMigration:        "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "info",
					SkipMigrations:     true,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "skip migrations string",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"	
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:      "debug",
					DatastoreEngine:        "cockroachdb",
					DatastoreURI:           "uri",
					SpannerCredsSecretRef:  "",
					TargetSpiceDBImage:     "image:v1",
					EnvPrefix:              "SPICEDB",
					SpiceDBCmd:             "spicedb",
					DatastoreTLSSecretName: "",
					TargetMigration:        "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "info",
					SkipMigrations:     true,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "set extra annotations as string",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"extraPodAnnotations": "app.kubernetes.io/name=test,app.kubernetes.io/managed-by=test-owner"
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image:v1",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:       "info",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
					ExtraPodAnnotations: map[string]string{
						"app.kubernetes.io/name":       "test",
						"app.kubernetes.io/managed-by": "test-owner",
					},
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "set extra annotations as map",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"extraPodAnnotations": {
							"app.kubernetes.io/name": "test",
							"app.kubernetes.io/managed-by": "test-owner"
						}
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image:v1",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:       "info",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
					ExtraPodAnnotations: map[string]string{
						"app.kubernetes.io/name":       "test",
						"app.kubernetes.io/managed-by": "test-owner",
					},
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "set extra service account with annotations as string",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
                        "serviceAccountName": "spicedb-non-default",
						"extraServiceAccountAnnotations": "iam.gke.io/gcp-service-account=authzed-operator@account-12345.iam.gserviceaccount.com"
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image:v1",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "info",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "spicedb-non-default",
					ExtraServiceAccountAnnotations: map[string]string{
						"iam.gke.io/gcp-service-account": "authzed-operator@account-12345.iam.gserviceaccount.com",
					},
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "set extra service account with annotations as map",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
                        "serviceAccountName": "spicedb-non-default",
						"extraServiceAccountAnnotations": {
							"iam.gke.io/gcp-service-account": "authzed-operator@account-12345.iam.gserviceaccount.com"
						}
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:  "debug",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image:v1",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					TargetMigration:    "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "info",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "spicedb-non-default",
					ExtraServiceAccountAnnotations: map[string]string{
						"iam.gke.io/gcp-service-account": "authzed-operator@account-12345.iam.gserviceaccount.com",
					},
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "set different migration and spicedb log level",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"migrationLogLevel": "info",
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"	
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:      "info",
					DatastoreEngine:        "cockroachdb",
					DatastoreURI:           "uri",
					SpannerCredsSecretRef:  "",
					TargetSpiceDBImage:     "image:v1",
					EnvPrefix:              "SPICEDB",
					SpiceDBCmd:             "spicedb",
					DatastoreTLSSecretName: "",
					TargetMigration:        "head",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "debug",
					SkipMigrations:     true,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=debug",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "update graph pushes the current version forward",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"migrationLogLevel": "info",
						"datastoreEngine": "cockroachdb"
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
								Nodes: []updates.State{
									{ID: "v2", Tag: "v2", Migration: "migration1", Phase: "phase1"},
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {"v2"}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:      "info",
					DatastoreEngine:        "cockroachdb",
					DatastoreURI:           "uri",
					SpannerCredsSecretRef:  "",
					TargetSpiceDBImage:     "image:v2",
					EnvPrefix:              "SPICEDB",
					SpiceDBCmd:             "spicedb",
					DatastoreTLSSecretName: "",
					TargetMigration:        "migration1",
					TargetPhase:            "phase1",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v2",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "debug",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":         "cockroachdb",
						"datastoreMigrationPhase": "phase1",
						"dispatchClusterEnabled":  "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=debug",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DATASTORE_MIGRATION_PHASE=phase1",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "explicit channel and version, updates to the next in the channel",
			args: args{
				cluster: v1alpha1.ClusterSpec{
					Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"migrationLogLevel": "info",
						"datastoreEngine": "cockroachdb"
					}
				`),
					Channel: "cockroachdb",
					Version: "v2",
				},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v2", Tag: "v2", Migration: "migration1", Phase: "phase1"},
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {"v2"}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:      "info",
					DatastoreEngine:        "cockroachdb",
					DatastoreURI:           "uri",
					SpannerCredsSecretRef:  "",
					TargetSpiceDBImage:     "image:v2",
					EnvPrefix:              "SPICEDB",
					SpiceDBCmd:             "spicedb",
					DatastoreTLSSecretName: "",
					TargetMigration:        "migration1",
					TargetPhase:            "phase1",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v2",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "debug",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":         "cockroachdb",
						"datastoreMigrationPhase": "phase1",
						"dispatchClusterEnabled":  "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=debug",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DATASTORE_MIGRATION_PHASE=phase1",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "explicit channel and version, doesn't update past the explicit version",
			args: args{
				cluster: v1alpha1.ClusterSpec{
					Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"migrationLogLevel": "info",
						"datastoreEngine": "cockroachdb"
					}
				`),
					Channel: "cockroachdb",
					Version: "v2",
				},
				status: v1alpha1.ClusterStatus{
					CurrentVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v2",
						Channel: "cockroachdb",
					},
				},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v3", Tag: "v3", Migration: "migration2", Phase: "phase2"},
									{ID: "v2", Tag: "v2", Migration: "migration1", Phase: "phase1"},
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {"v2", "v3"}, "v2": {"v3"}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:      "info",
					DatastoreEngine:        "cockroachdb",
					DatastoreURI:           "uri",
					SpannerCredsSecretRef:  "",
					TargetSpiceDBImage:     "image:v2",
					EnvPrefix:              "SPICEDB",
					SpiceDBCmd:             "spicedb",
					DatastoreTLSSecretName: "",
					TargetMigration:        "migration1",
					TargetPhase:            "phase1",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v2",
						Channel: "cockroachdb",
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "debug",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":         "cockroachdb",
						"datastoreMigrationPhase": "phase1",
						"dispatchClusterEnabled":  "true",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=debug",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DATASTORE_MIGRATION_PHASE=phase1",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
		{
			name: "set spanner credentials",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "spanner",
						"spannerCredentials": "spanner-creds-secret-name"
					}
				`)},
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "spanner",
								Metadata: map[string]string{"datastore": "spanner", "default": "true"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			want: &Config{
				MigrationConfig: MigrationConfig{
					MigrationLogLevel:     "debug",
					DatastoreEngine:       "spanner",
					DatastoreURI:          "uri",
					TargetSpiceDBImage:    "image:v1",
					EnvPrefix:             "SPICEDB",
					SpiceDBCmd:            "spicedb",
					TargetMigration:       "head",
					SpannerCredsSecretRef: "spanner-creds-secret-name",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "spanner",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:           "info",
					SkipMigrations:     false,
					Name:               "test",
					Namespace:          "test",
					UID:                "1",
					Replicas:           2,
					PresharedKey:       "psk",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
					ServiceAccountName: "test",
					Passthrough: map[string]string{
						"datastoreEngine":             "spanner",
						"dispatchClusterEnabled":      "true",
						"datastoreSpannerCredentials": "/spanner-credentials/credentials.json",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=spanner",
				"SPICEDB_DATASTORE_SPANNER_CREDENTIALS=/spanner-credentials/credentials.json",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
			},
			wantPortCount: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			global := tt.args.globalConfig.Copy()
			cluster := &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					UID:       types.UID("1"),
				},
				Spec:   tt.args.cluster,
				Status: tt.args.status,
			}
			got, gotWarning, err := NewConfig(cluster, &global, tt.args.secret)
			require.EqualValues(t, errors.NewAggregate(tt.wantErrs), err)
			require.EqualValues(t, errors.NewAggregate(tt.wantWarnings), gotWarning)
			require.Equal(t, tt.want, got)

			if got != nil {
				gotEnvs := got.toEnvVarApplyConfiguration()
				wantEnvs := envVarFromStrings(tt.wantEnvs)
				require.Equal(t, wantEnvs, gotEnvs)

				require.Equal(t, tt.wantPortCount, len(got.servicePorts()),
					"expected service to have %d ports but had %d", tt.wantPortCount, len(got.servicePorts()))
				require.Equal(t, tt.wantPortCount, len(got.containerPorts()),
					"expected container to have %d ports but had %d", tt.wantPortCount, len(got.containerPorts()))
			}
		})
	}
}

// envs that will be mapped back to ENV vars
var secrets = map[string]struct{}{
	"SPICEDB_GRPC_PRESHARED_KEY": {},
	"SPICEDB_DATASTORE_CONN_URI": {},
}

func envVarFromStrings(envs []string) []*v1.EnvVarApplyConfiguration {
	vars := make([]*v1.EnvVarApplyConfiguration, 0, len(envs))
	for _, env := range envs {
		name, value, _ := strings.Cut(env, "=")
		var valueFrom *v1.EnvVarSourceApplyConfiguration
		var valuePtr *string
		if value != "" {
			valuePtr = &value
		}
		// hack for the sake of simplifying test fixtures
		// if it's lowercase key, we assume it's a secret
		if _, ok := secrets[name]; ok {
			localname := ""
			valueFrom = &v1.EnvVarSourceApplyConfiguration{
				SecretKeyRef: &v1.SecretKeySelectorApplyConfiguration{
					LocalObjectReferenceApplyConfiguration: v1.LocalObjectReferenceApplyConfiguration{
						Name: &localname,
					},
					Key: valuePtr,
				},
			}
			valuePtr = nil
		}
		vars = append(vars, &v1.EnvVarApplyConfiguration{
			Name:      &name,
			Value:     valuePtr,
			ValueFrom: valueFrom,
		})
	}
	return vars
}

func TestPatchesApplyToAllObjects(t *testing.T) {
	config := &Config{}
	configType := reflect.TypeOf(config)
	for i := 0; i < configType.NumMethod(); i++ {
		method := configType.Method(i)

		// Every public method of Config should return an object
		// that supports patching
		t.Run(method.Name, func(t *testing.T) {
			config.Patches = []v1alpha1.Patch{}

			// all args are strings
			args := []reflect.Value{reflect.ValueOf(config)}
			for i := 1; i < method.Type.NumIn(); i++ {
				args = append(args, reflect.ValueOf("testtesttesttesttesttest"))
			}

			object := method.Func.Call(args)[0]
			initialBytes, err := json.Marshal(object.Interface())
			require.NoError(t, err)

			config.Patches = []v1alpha1.Patch{
				{
					Kind:  wildcard,
					Patch: json.RawMessage(`{"op": "add", "path": "/metadata/labels", "value":{"added":"via-patch"}}`),
				},
			}
			after := method.Func.Call(args)[0]
			afterBytes, err := json.Marshal(after.Interface())
			require.NoError(t, err)

			require.NotEqual(t, initialBytes, afterBytes)
			require.True(t, bytes.Contains(afterBytes, []byte("via-patch")))
		})
	}
}
