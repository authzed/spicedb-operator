package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
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
			if got := ToEnvVarName(tt.prefix, tt.key); got != tt.want {
				t.Errorf("ToEnvVarName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewConfig(t *testing.T) {
	type args struct {
		nn               types.NamespacedName
		uid              types.UID
		version, channel string
		currentVersion   *v1alpha1.SpiceDBVersion
		globalConfig     OperatorConfig
		rawConfig        json.RawMessage
		secret           *corev1.Secret
		rolling          bool
	}
	tests := []struct {
		name         string
		args         args
		want         *Config
		wantEnvs     []string
		wantWarnings []error
		wantErrs     []error
	}{
		{
			name: "missing required",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
				},
				rawConfig: json.RawMessage(`
					{
						"test": "field"
					}
				`),
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
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb"
					}
				`),
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
		},
		{
			name: "memory",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "memory",
								Metadata: map[string]string{"datastore": "memory"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "memory"
					}
				`),
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
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:       "info",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       1,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
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
		},
		{
			name: "set image with tag explicitly",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"image": "adifferentimage:tag"
					}
				`),
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
					LogLevel:       "info",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
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
		},
		{
			name: "set image with digest explicitly",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"image": "adifferentimage@sha256:abc"
					}
				`),
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
					LogLevel:       "info",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
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
		},
		{
			name: "set replicas as int",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"replicas": 3
					}
				`),
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
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:       "info",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       3,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
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
		},
		{
			name: "set replicas as string",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"replicas": "3"
					}
				`),
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
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:       "info",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       3,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
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
		},
		{
			name: "set extra labels as string",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"extraPodLabels": "test=label,other=label"
					}
				`),
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
		},
		{
			name: "set extra labels as map",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"extraPodLabels": {
							"test": "label",
							"other": "label"
						}
					}
				`),
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
		},
		{
			name: "skip migrations bool",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"skipMigrations": true	
					}
				`),
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
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:       "info",
					SkipMigrations: true,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
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
		},
		{
			name: "skip migrations string",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"	
					}
				`),
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
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:       "info",
					SkipMigrations: true,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
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
		},
		{
			name: "set extra annotations as string",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"extraPodAnnotations": "app.kubernetes.io/name=test,app.kubernetes.io/managed-by=test-owner"
					}
				`),
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
		},
		{
			name: "set extra annotations as map",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"extraPodAnnotations": {
							"app.kubernetes.io/name": "test",
							"app.kubernetes.io/managed-by": "test-owner"
						}
					}
				`),
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
		},
		{
			name: "set different migration and spicedb log level",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
				globalConfig: OperatorConfig{
					ImageName: "image",
					UpdateGraph: updates.UpdateGraph{
						Channels: []updates.Channel{
							{
								Name:     "cockroachdb",
								Metadata: map[string]string{"datastore": "cockroachdb"},
								Nodes: []updates.State{
									{ID: "v1", Tag: "v1"},
								},
								Edges: map[string][]string{"v1": {}},
							},
						},
					},
				},
				rawConfig: json.RawMessage(`
					{
						"logLevel": "debug",
						"migrationLogLevel": "info",
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"	
					}
				`),
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
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:       "debug",
					SkipMigrations: true,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
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
		},
		{
			name: "update graph pushes the current version forward",
			args: args{
				nn:  types.NamespacedName{Namespace: "test", Name: "test"},
				uid: types.UID("1"),
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
				rawConfig: json.RawMessage(`
					{
						"logLevel": "debug",
						"migrationLogLevel": "info",
						"datastoreEngine": "cockroachdb"
					}
				`),
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
					LogLevel:       "debug",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
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
		},
		{
			name: "explicit channel and version, updates to the next in the channel",
			args: args{
				nn:      types.NamespacedName{Namespace: "test", Name: "test"},
				uid:     types.UID("1"),
				channel: "cockroachdb",
				version: "v2",
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
				rawConfig: json.RawMessage(`
					{
						"logLevel": "debug",
						"migrationLogLevel": "info",
						"datastoreEngine": "cockroachdb"
					}
				`),
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
					LogLevel:       "debug",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
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
		},
		{
			name: "explicit channel and version, doesn't update past the explicit version",
			args: args{
				nn:      types.NamespacedName{Namespace: "test", Name: "test"},
				uid:     types.UID("1"),
				channel: "cockroachdb",
				version: "v2",
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
				currentVersion: &v1alpha1.SpiceDBVersion{
					Name:    "v2",
					Channel: "cockroachdb",
				},
				rawConfig: json.RawMessage(`
					{
						"logLevel": "debug",
						"migrationLogLevel": "info",
						"datastoreEngine": "cockroachdb"
					}
				`),
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
					LogLevel:       "debug",
					SkipMigrations: false,
					Name:           "test",
					Namespace:      "test",
					UID:            "1",
					Replicas:       2,
					PresharedKey:   "psk",
					EnvPrefix:      "SPICEDB",
					SpiceDBCmd:     "spicedb",
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
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			global := tt.args.globalConfig.Copy()

			got, gotWarning, err := NewConfig(tt.args.nn, tt.args.uid, tt.args.version, tt.args.channel, tt.args.currentVersion, &global, tt.args.rawConfig, tt.args.secret, tt.args.rolling)
			require.EqualValues(t, errors.NewAggregate(tt.wantErrs), err)
			require.EqualValues(t, errors.NewAggregate(tt.wantWarnings), gotWarning)
			require.Equal(t, tt.want, got)

			if got != nil {
				gotEnvs := got.ToEnvVarApplyConfiguration()
				wantEnvs := envVarFromStrings(tt.wantEnvs)
				require.Equal(t, wantEnvs, gotEnvs)
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
