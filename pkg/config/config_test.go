package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/intstr"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	applyappsv1 "k8s.io/client-go/applyconfigurations/apps/v1"
	applybatchv1 "k8s.io/client-go/applyconfigurations/batch/v1"
	applycorev1 "k8s.io/client-go/applyconfigurations/core/v1"
	applymetav1 "k8s.io/client-go/applyconfigurations/meta/v1"
	applypolicyv1 "k8s.io/client-go/applyconfigurations/policy/v1"
	applyrbacv1 "k8s.io/client-go/applyconfigurations/rbac/v1"
	openapitesting "k8s.io/kubectl/pkg/util/openapi/testing"
	"k8s.io/utils/ptr"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
	"github.com/authzed/spicedb-operator/pkg/metadata"
	"github.com/authzed/spicedb-operator/pkg/updates"
)

func newFakeResources() *openapitesting.FakeResources {
	return openapitesting.NewFakeResources(filepath.Join("testdata", "swagger.1.26.3.json"))
}

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
	resources := newFakeResources()
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
					LogLevel:                     "info",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
			},
			wantPortCount: 4,
		},
		{
			name: "override termination log",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
                        "terminationLogPath": "/alt/path"
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
					LogLevel:                     "info",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/alt/path",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/alt/path",
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
					LogLevel:                     "info",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     1,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              false,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "memory",
						"dispatchClusterEnabled": "false",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_ENGINE=memory",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=false",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					LogLevel:                     "info",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					LogLevel:                     "info",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					LogLevel:                     "info",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     3,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					LogLevel:                     "info",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     3,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					LogLevel:                     "info",
					SkipMigrations:               true,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					LogLevel:                     "info",
					SkipMigrations:               true,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					LogLevel:                     "debug",
					SkipMigrations:               true,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=debug",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
			},
			wantPortCount: 4,
		},
		{
			name: "disable dispatch",
			args: args{
				cluster: v1alpha1.ClusterSpec{Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"dispatchEnabled": false,
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
					LogLevel:                     "debug",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              false,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "false",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=debug",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=false",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
			},
			wantPortCount: 3,
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
					LogLevel:                     "debug",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":         "cockroachdb",
						"datastoreMigrationPhase": "phase1",
						"dispatchClusterEnabled":  "true",
						"terminationLogPath":      "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=debug",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DATASTORE_MIGRATION_PHASE=phase1",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					LogLevel:                     "debug",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":         "cockroachdb",
						"datastoreMigrationPhase": "phase1",
						"dispatchClusterEnabled":  "true",
						"terminationLogPath":      "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=debug",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DATASTORE_MIGRATION_PHASE=phase1",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					LogLevel:                     "debug",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":         "cockroachdb",
						"datastoreMigrationPhase": "phase1",
						"dispatchClusterEnabled":  "true",
						"terminationLogPath":      "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=debug",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DATASTORE_MIGRATION_PHASE=phase1",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
					LogLevel:                     "info",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":             "spanner",
						"dispatchClusterEnabled":      "true",
						"datastoreSpannerCredentials": "/spanner-credentials/credentials.json",
						"terminationLogPath":          "/dev/termination-log",
					},
				},
			},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=spanner",
				"SPICEDB_DATASTORE_SPANNER_CREDENTIALS=/spanner-credentials/credentials.json",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
			},
			wantPortCount: 4,
		},
		{
			name: "custom base image from cluster spec",
			args: args{
				cluster: v1alpha1.ClusterSpec{
					BaseImage: "public.ecr.aws/authzed/spicedb",
					Config: json.RawMessage(`
						{
							"datastoreEngine": "cockroachdb"
						}
					`),
				},
				globalConfig: OperatorConfig{
					ImageName: "public.ecr.aws/authzed/spicedb",
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
			want: &Config{
				MigrationConfig: MigrationConfig{
					TargetMigration:        "head",
					TargetPhase:            "",
					MigrationLogLevel:      "debug",
					DatastoreEngine:        "cockroachdb",
					DatastoreURI:           "uri",
					SpannerCredsSecretRef:  "",
					TargetSpiceDBImage:     "public.ecr.aws/authzed/spicedb:v1",
					EnvPrefix:              "SPICEDB",
					SpiceDBCmd:             "spicedb",
					DatastoreTLSSecretName: "",
					SpiceDBVersion: &v1alpha1.SpiceDBVersion{
						Name:    "v1",
						Channel: "cockroachdb",
						Attributes: []v1alpha1.SpiceDBVersionAttributes{
							v1alpha1.SpiceDBVersionAttributesMigration,
						},
					},
				},
				SpiceConfig: SpiceConfig{
					LogLevel:                     "info",
					SkipMigrations:               false,
					Name:                         "test",
					Namespace:                    "test",
					UID:                          "1",
					Replicas:                     2,
					PresharedKey:                 "psk",
					EnvPrefix:                    "SPICEDB",
					SpiceDBCmd:                   "spicedb",
					ServiceAccountName:           "test",
					DispatchEnabled:              true,
					DispatchUpstreamCASecretPath: "tls.crt",
					ProjectLabels:                true,
					ProjectAnnotations:           true,
					Passthrough: map[string]string{
						"datastoreEngine":        "cockroachdb",
						"dispatchClusterEnabled": "true",
						"terminationLogPath":     "/dev/termination-log",
					},
				},
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
			wantEnvs: []string{
				"SPICEDB_POD_NAME=FIELD_REF=metadata.name",
				"SPICEDB_LOG_LEVEL=info",
				"SPICEDB_GRPC_PRESHARED_KEY=preshared_key",
				"SPICEDB_DATASTORE_CONN_URI=datastore_uri",
				"SPICEDB_DISPATCH_UPSTREAM_ADDR=kubernetes:///test.test:dispatch",
				"SPICEDB_DATASTORE_ENGINE=cockroachdb",
				"SPICEDB_DISPATCH_CLUSTER_ENABLED=true",
				"SPICEDB_TERMINATION_LOG_PATH=/dev/termination-log",
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
			if tt.want != nil {
				tt.want.Resources = resources
			}
			got, gotWarning, err := NewConfig(cluster, &global, tt.args.secret, resources)
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

func envVarFromStrings(envs []string) []*applycorev1.EnvVarApplyConfiguration {
	vars := make([]*applycorev1.EnvVarApplyConfiguration, 0, len(envs))
	for _, env := range envs {
		name, value, _ := strings.Cut(env, "=")
		var valueFrom *applycorev1.EnvVarSourceApplyConfiguration
		var valuePtr *string
		if value != "" {
			valuePtr = &value
		}

		if _, ref, ok := strings.Cut(value, "FIELD_REF="); ok {
			valueFrom = &applycorev1.EnvVarSourceApplyConfiguration{
				FieldRef: &applycorev1.ObjectFieldSelectorApplyConfiguration{
					FieldPath: &ref,
				},
			}
			valuePtr = nil
		}

		if _, ok := secrets[name]; ok {
			localname := ""
			valueFrom = &applycorev1.EnvVarSourceApplyConfiguration{
				SecretKeyRef: &applycorev1.SecretKeySelectorApplyConfiguration{
					LocalObjectReferenceApplyConfiguration: applycorev1.LocalObjectReferenceApplyConfiguration{
						Name: &localname,
					},
					Key: valuePtr,
				},
			}
			valuePtr = nil
		}
		vars = append(vars, &applycorev1.EnvVarApplyConfiguration{
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

func GetConfig(fileName string) (cfg OperatorConfig) {
	file, err := os.Open(fileName)
	if err != nil {
		panic(err)
	}
	defer file.Close()
	decoder := utilyaml.NewYAMLOrJSONDecoder(file, 100)
	err = decoder.Decode(&cfg)
	if err != nil {
		panic(err)
	}
	return
}

func TestGraphDiffSanity(t *testing.T) {
	proposedGraph := GetConfig("../../proposed-update-graph.yaml")
	validatedGraph := GetConfig("../../config/update-graph.yaml")
	require.NotPanics(t, func() {
		_ = proposedGraph.Difference(&validatedGraph.UpdateGraph)
	})
}

func TestDeployment(t *testing.T) {
	resources := newFakeResources()
	tests := []struct {
		name           string
		cluster        v1alpha1.ClusterSpec
		secret         *corev1.Secret
		wantDeployment *applyappsv1.DeploymentApplyConfiguration
	}{
		{
			name: "container name back compat: smp patch with old name",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "Deployment",
					Patch: json.RawMessage(`
spec:
  template:
    spec:
      containers:
      - name: test-spicedb
        resources:
          requests:
            memory: "64Mi"
            cpu: "250m"
          limits:
            memory: "128Mi"
            cpu: "500m"
`),
				}},
			},
			secret: &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}},
			wantDeployment: expectedDeployment(func(dep *applyappsv1.DeploymentApplyConfiguration) {
				dep.Spec.Template.Spec.Containers[0].WithResources(applycorev1.ResourceRequirements().
					WithRequests(corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("64Mi"),
						corev1.ResourceCPU:    resource.MustParse("250m"),
					}).
					WithLimits(corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("128Mi"),
						corev1.ResourceCPU:    resource.MustParse("500m"),
					}))
			}),
		},
		{
			name: "container name back compat: smp wildcard patch with old name",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "*",
					Patch: json.RawMessage(`
spec:
  template:
    spec:
      containers:
      - name: test-spicedb
        resources:
          requests:
            memory: "64Mi"
            cpu: "250m"
          limits:
            memory: "128Mi"
            cpu: "500m"
`),
				}},
			},
			secret: &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}},
			wantDeployment: expectedDeployment(func(dep *applyappsv1.DeploymentApplyConfiguration) {
				dep.Spec.Template.Spec.Containers[0].WithResources(applycorev1.ResourceRequirements().
					WithRequests(corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("64Mi"),
						corev1.ResourceCPU:    resource.MustParse("250m"),
					}).
					WithLimits(corev1.ResourceList{
						corev1.ResourceMemory: resource.MustParse("128Mi"),
						corev1.ResourceCPU:    resource.MustParse("500m"),
					}))
			}),
		},
		{
			name: "patch preserves required fields",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "Deployment",
					Patch: json.RawMessage(`
metadata:
  labels: null
`),
				}},
			},
			secret: &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}},
			wantDeployment: expectedDeployment(func(dep *applyappsv1.DeploymentApplyConfiguration) {
				dep.Labels = metadata.LabelsForComponent("test", metadata.ComponentSpiceDBLabelValue)
			}),
		},
		{
			name: "patch would create invalid deployment: missing selector",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "Deployment",
					Patch: json.RawMessage(`
      spec:
        selector: null
`),
				}},
			},
			secret: &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}},
			wantDeployment: expectedDeployment(),
		},
		{
			name: "patch would create invalid deployment: missing spec",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "Deployment",
					Patch: json.RawMessage(`
      spec: null
`),
				}},
			},
			secret: &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}},
			wantDeployment: expectedDeployment(),
		},
		{
			name: "patch would create invalid deployment: missing template",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "Deployment",
					Patch: json.RawMessage(`
      spec: 
        template: null
`),
				}},
			},
			secret: &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}},
			wantDeployment: expectedDeployment(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cluster := &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					UID:       types.UID("1"),
				},
				Spec: tt.cluster,
			}
			got, _, err := NewConfig(cluster, ptr.To(testGlobalConfig.Copy()), tt.secret, resources)
			require.NoError(t, err)

			wantDep, err := json.Marshal(tt.wantDeployment)
			require.NoError(t, err)
			gotDep, err := json.Marshal(got.Deployment("1", "2"))
			require.NoError(t, err)

			require.JSONEq(t, string(wantDep), string(gotDep))
		})
	}
}

func TestMigrationJob(t *testing.T) {
	resources := newFakeResources()
	tests := []struct {
		name    string
		cluster v1alpha1.ClusterSpec
		wantJob *applybatchv1.JobApplyConfiguration
	}{
		{
			name: "patch preserves required fields",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "Job",
					Patch: json.RawMessage(`
metadata:
  labels: null
`),
				}},
			},
			wantJob: expectedJob(func(job *applybatchv1.JobApplyConfiguration) {
				job.Labels = metadata.LabelsForComponent("test", metadata.ComponentMigrationJobLabelValue)
			}),
		},
		{
			name: "patch would create invalid job: missing template",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "Job",
					Patch: json.RawMessage(`
      spec: 
        template: null
`),
				}},
			},
			wantJob: expectedJob(),
		},
		{
			name: "patch would create invalid job: missing spec",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "Job",
					Patch: json.RawMessage(`
      spec: null
`),
				}},
			},
			wantJob: expectedJob(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}}
			cluster := &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					UID:       types.UID("1"),
				},
				Spec: tt.cluster,
			}
			got, _, err := NewConfig(cluster, ptr.To(testGlobalConfig.Copy()), secret, resources)
			require.NoError(t, err)

			wantJob, err := json.Marshal(tt.wantJob)
			require.NoError(t, err)
			gotJob, err := json.Marshal(got.MigrationJob("1"))
			require.NoError(t, err)

			require.JSONEq(t, string(wantJob), string(gotJob))
		})
	}
}

func TestService(t *testing.T) {
	resources := newFakeResources()
	tests := []struct {
		name        string
		cluster     v1alpha1.ClusterSpec
		wantService *applycorev1.ServiceApplyConfiguration
	}{
		{
			name: "patches preserve required fields",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "Service",
					Patch: json.RawMessage(`
metadata:
  labels: null
`),
				}},
			},
			wantService: applycorev1.Service("test", "test").
				WithLabels(metadata.LabelsForComponent("test", metadata.ComponentServiceLabel)).
				WithOwnerReferences(applymetav1.OwnerReference().
					WithName("test").
					WithKind(v1alpha1.SpiceDBClusterKind).
					WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
					WithUID("1")).
				WithSpec(applycorev1.ServiceSpec().
					WithSelector(metadata.LabelsForComponent("test", metadata.ComponentSpiceDBLabelValue)).
					WithPorts(
						applycorev1.ServicePort().WithName("grpc").WithPort(50051),
						applycorev1.ServicePort().WithName("gateway").WithPort(8443),
						applycorev1.ServicePort().WithName("metrics").WithPort(9090),
						applycorev1.ServicePort().WithName("dispatch").WithPort(50053),
					),
				),
		},
		{
			name: "patch would create invalid service: missing spec",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb",
						"skipMigrations": "true"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "Service",
					Patch: json.RawMessage(`
      spec: null
`),
				}},
			},
			wantService: applycorev1.Service("test", "test").
				WithLabels(metadata.LabelsForComponent("test", metadata.ComponentServiceLabel)).
				WithLabels(map[string]string{
					metadata.KubernetesInstanceLabelKey:  "test",
					metadata.KubernetesNameLabelKey:      "test",
					metadata.KubernetesComponentLabelKey: metadata.ComponentSpiceDBLabelValue,
					metadata.KubernetesVersionLabelKey:   "v1",
				}).
				WithOwnerReferences(applymetav1.OwnerReference().
					WithName("test").
					WithKind(v1alpha1.SpiceDBClusterKind).
					WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
					WithUID("1")).
				WithSpec(applycorev1.ServiceSpec().
					WithSelector(metadata.LabelsForComponent("test", metadata.ComponentSpiceDBLabelValue)).
					WithPorts(
						applycorev1.ServicePort().WithName("grpc").WithPort(50051),
						applycorev1.ServicePort().WithName("gateway").WithPort(8443),
						applycorev1.ServicePort().WithName("metrics").WithPort(9090),
						applycorev1.ServicePort().WithName("dispatch").WithPort(50053),
					),
				),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}}
			cluster := &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					UID:       types.UID("1"),
				},
				Spec: tt.cluster,
			}
			got, _, err := NewConfig(cluster, ptr.To(testGlobalConfig.Copy()), secret, resources)
			require.NoError(t, err)

			wantService, err := json.Marshal(tt.wantService)
			require.NoError(t, err)
			gotService, err := json.Marshal(got.Service())
			require.NoError(t, err)

			require.JSONEq(t, string(wantService), string(gotService))
		})
	}
}

func TestRole(t *testing.T) {
	resources := newFakeResources()
	tests := []struct {
		name     string
		cluster  v1alpha1.ClusterSpec
		wantRole *applyrbacv1.RoleApplyConfiguration
	}{
		{
			name: "patches preserve required fields",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "Role",
					Patch: json.RawMessage(`
metadata:
  labels: null
`),
				}},
			},
			wantRole: applyrbacv1.Role("test", "test").
				WithLabels(metadata.LabelsForComponent("test", metadata.ComponentRoleLabel)).
				WithOwnerReferences(applymetav1.OwnerReference().
					WithName("test").
					WithKind(v1alpha1.SpiceDBClusterKind).
					WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
					WithUID("1")).
				WithRules(
					applyrbacv1.PolicyRule().
						WithAPIGroups("").
						WithResources("endpoints").
						WithVerbs("get", "list", "watch"),
				),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}}
			cluster := &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					UID:       types.UID("1"),
				},
				Spec: tt.cluster,
			}
			got, _, err := NewConfig(cluster, ptr.To(testGlobalConfig.Copy()), secret, resources)
			require.NoError(t, err)

			wantRole, err := json.Marshal(tt.wantRole)
			require.NoError(t, err)
			gotRole, err := json.Marshal(got.Role())
			require.NoError(t, err)

			require.JSONEq(t, string(wantRole), string(gotRole))
		})
	}
}

func TestRoleBinding(t *testing.T) {
	resources := newFakeResources()
	tests := []struct {
		name            string
		cluster         v1alpha1.ClusterSpec
		wantRoleBinding *applyrbacv1.RoleBindingApplyConfiguration
	}{
		{
			name: "patches preserve required fields",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "RoleBinding",
					Patch: json.RawMessage(`
metadata:
  labels: null
`),
				}},
			},
			wantRoleBinding: applyrbacv1.RoleBinding("test", "test").
				WithLabels(metadata.LabelsForComponent("test", metadata.ComponentRoleBindingLabel)).
				WithOwnerReferences(applymetav1.OwnerReference().
					WithName("test").
					WithKind(v1alpha1.SpiceDBClusterKind).
					WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
					WithUID("1")).
				WithRoleRef(applyrbacv1.RoleRef().
					WithKind("Role").
					WithName("test"),
				).WithSubjects(applyrbacv1.Subject().
				WithNamespace("test").
				WithKind("ServiceAccount").WithName("test"),
			),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}}
			cluster := &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					UID:       types.UID("1"),
				},
				Spec: tt.cluster,
			}
			got, _, err := NewConfig(cluster, ptr.To(testGlobalConfig.Copy()), secret, resources)
			require.NoError(t, err)

			wantRoleBinding, err := json.Marshal(tt.wantRoleBinding)
			require.NoError(t, err)
			gotRoleBinding, err := json.Marshal(got.RoleBinding())
			require.NoError(t, err)

			require.JSONEq(t, string(wantRoleBinding), string(gotRoleBinding))
		})
	}
}

func TestServiceAccount(t *testing.T) {
	resources := newFakeResources()
	tests := []struct {
		name               string
		cluster            v1alpha1.ClusterSpec
		wantServiceAccount *applycorev1.ServiceAccountApplyConfiguration
	}{
		{
			name: "patches preserve required fields",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "ServiceAccount",
					Patch: json.RawMessage(`
metadata:
  labels: null
`),
				}},
			},
			wantServiceAccount: applycorev1.ServiceAccount("test", "test").
				WithLabels(metadata.LabelsForComponent("test", metadata.ComponentServiceAccountLabel)).
				WithOwnerReferences(applymetav1.OwnerReference().
					WithName("test").
					WithKind(v1alpha1.SpiceDBClusterKind).
					WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
					WithUID("1")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}}
			cluster := &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					UID:       types.UID("1"),
				},
				Spec: tt.cluster,
			}
			got, _, err := NewConfig(cluster, ptr.To(testGlobalConfig.Copy()), secret, resources)
			require.NoError(t, err)

			wantServiceAccount, err := json.Marshal(tt.wantServiceAccount)
			require.NoError(t, err)
			gotServiceAccount, err := json.Marshal(got.ServiceAccount())
			require.NoError(t, err)

			require.JSONEq(t, string(wantServiceAccount), string(gotServiceAccount))
		})
	}
}

func TestPDB(t *testing.T) {
	resources := newFakeResources()
	tests := []struct {
		name    string
		cluster v1alpha1.ClusterSpec
		wantPDB *applypolicyv1.PodDisruptionBudgetApplyConfiguration
	}{
		{
			name: "pdb sets maxUnavailable to 1",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"replicas": 5,
						"datastoreEngine": "cockroachdb"
					}
				`),
			},
			wantPDB: applypolicyv1.PodDisruptionBudget("test-spicedb", "test").
				WithLabels(metadata.LabelsForComponent("test", metadata.ComponentPDBLabel)).
				WithLabels(map[string]string{
					metadata.KubernetesInstanceLabelKey:  "test-spicedb",
					metadata.KubernetesNameLabelKey:      "test-spicedb",
					metadata.KubernetesComponentLabelKey: metadata.ComponentSpiceDBLabelValue,
					metadata.KubernetesVersionLabelKey:   "v1",
				}).
				WithOwnerReferences(applymetav1.OwnerReference().
					WithName("test").
					WithKind(v1alpha1.SpiceDBClusterKind).
					WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
					WithUID("1")).
				WithSpec(
					applypolicyv1.PodDisruptionBudgetSpec().WithSelector(
						applymetav1.LabelSelector().WithMatchLabels(map[string]string{
							metadata.KubernetesInstanceLabelKey: "test-spicedb",
						}),
					).WithMaxUnavailable(intstr.FromInt32(1))),
		},
		{
			name: "patches preserve required fields",
			cluster: v1alpha1.ClusterSpec{
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb"
					}
				`),
				Patches: []v1alpha1.Patch{{
					Kind: "PodDisruptionBudget",
					Patch: json.RawMessage(`
metadata:
  labels: null
`),
				}},
			},
			wantPDB: applypolicyv1.PodDisruptionBudget("test-spicedb", "test").
				WithLabels(metadata.LabelsForComponent("test", metadata.ComponentPDBLabel)).
				WithLabels(map[string]string{
					metadata.KubernetesInstanceLabelKey:  "test-spicedb",
					metadata.KubernetesNameLabelKey:      "test-spicedb",
					metadata.KubernetesComponentLabelKey: metadata.ComponentSpiceDBLabelValue,
					metadata.KubernetesVersionLabelKey:   "v1",
				}).
				WithOwnerReferences(applymetav1.OwnerReference().
					WithName("test").
					WithKind(v1alpha1.SpiceDBClusterKind).
					WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
					WithUID("1")).
				WithSpec(
					applypolicyv1.PodDisruptionBudgetSpec().WithSelector(
						applymetav1.LabelSelector().WithMatchLabels(map[string]string{
							metadata.KubernetesInstanceLabelKey: "test-spicedb",
						}),
					).WithMaxUnavailable(intstr.FromInt32(1))),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}}
			cluster := &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					UID:       types.UID("1"),
				},
				Spec: tt.cluster,
			}
			got, _, err := NewConfig(cluster, ptr.To(testGlobalConfig.Copy()), secret, resources)
			require.NoError(t, err)

			wantPDB, err := json.Marshal(tt.wantPDB)
			require.NoError(t, err)
			gotPDB, err := json.Marshal(got.PodDisruptionBudget())
			require.NoError(t, err)

			require.JSONEq(t, string(wantPDB), string(gotPDB))
		})
	}
}

func TestVersionLabels(t *testing.T) {
	resources := newFakeResources()
	tests := []struct {
		name               string
		cluster            v1alpha1.ClusterSpec
		wantServiceAccount *applycorev1.ServiceAccountApplyConfiguration
	}{
		{
			name: "version label: slugify",
			cluster: v1alpha1.ClusterSpec{
				Version: "v1.0.0+test.v1",
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb"
					}
				`),
			},
			wantServiceAccount: applycorev1.ServiceAccount("test", "test").
				WithLabels(metadata.LabelsForComponent("test", metadata.ComponentServiceAccountLabel)).
				WithLabels(map[string]string{
					metadata.KubernetesInstanceLabelKey:  "test",
					metadata.KubernetesNameLabelKey:      "test",
					metadata.KubernetesComponentLabelKey: metadata.ComponentSpiceDBLabelValue,
					metadata.KubernetesVersionLabelKey:   "v1.0.0-test.v1",
				}).
				WithOwnerReferences(applymetav1.OwnerReference().
					WithName("test").
					WithKind(v1alpha1.SpiceDBClusterKind).
					WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
					WithUID("1")),
		},
		{
			name: "version label: shorten",
			cluster: v1alpha1.ClusterSpec{
				Version: "long64charstring-4567890abcdef1234567890abcdef1234567890abcdef12",
				Config: json.RawMessage(`
					{
						"logLevel": "debug",
						"datastoreEngine": "cockroachdb"
					}
				`),
			},
			wantServiceAccount: applycorev1.ServiceAccount("test", "test").
				WithLabels(metadata.LabelsForComponent("test", metadata.ComponentServiceAccountLabel)).
				WithLabels(map[string]string{
					metadata.KubernetesInstanceLabelKey:  "test",
					metadata.KubernetesNameLabelKey:      "test",
					metadata.KubernetesComponentLabelKey: metadata.ComponentSpiceDBLabelValue,
					metadata.KubernetesVersionLabelKey:   "long64charstring-4567890abcdef1234567890abcdef1234567890abcdef1",
				}).
				WithOwnerReferences(applymetav1.OwnerReference().
					WithName("test").
					WithKind(v1alpha1.SpiceDBClusterKind).
					WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
					WithUID("1")),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secret := &corev1.Secret{Data: map[string][]byte{
				"datastore_uri": []byte("uri"),
				"preshared_key": []byte("psk"),
			}}
			cluster := &v1alpha1.SpiceDBCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "test",
					UID:       types.UID("1"),
				},
				Spec: tt.cluster,
			}
			got, _, err := NewConfig(cluster, ptr.To(testGlobalConfig.Copy()), secret, resources)
			require.NoError(t, err)

			wantServiceAccount, err := json.Marshal(tt.wantServiceAccount)
			require.NoError(t, err)
			gotServiceAccount, err := json.Marshal(got.ServiceAccount())
			require.NoError(t, err)

			require.JSONEq(t, string(wantServiceAccount), string(gotServiceAccount))
		})
	}
}

func expectedDeployment(apply ...func(dep *applyappsv1.DeploymentApplyConfiguration)) *applyappsv1.DeploymentApplyConfiguration {
	base := applyappsv1.Deployment("test-spicedb", "test").
		WithLabels(metadata.LabelsForComponent("test", metadata.ComponentSpiceDBLabelValue)).
		WithLabels(map[string]string{
			metadata.KubernetesInstanceLabelKey:  "test-spicedb",
			metadata.KubernetesNameLabelKey:      "test-spicedb",
			metadata.KubernetesComponentLabelKey: metadata.ComponentSpiceDBLabelValue,
			metadata.KubernetesVersionLabelKey:   "v1",
		}).
		WithAnnotations(map[string]string{
			metadata.SpiceDBMigrationRequirementsKey: "1",
		}).
		WithOwnerReferences(applymetav1.OwnerReference().
			WithName("test").
			WithKind(v1alpha1.SpiceDBClusterKind).
			WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
			WithUID("1")).
		WithSpec(applyappsv1.DeploymentSpec().
			WithReplicas(2).
			WithStrategy(applyappsv1.DeploymentStrategy().
				WithType(appsv1.RollingUpdateDeploymentStrategyType).
				WithRollingUpdate(applyappsv1.RollingUpdateDeployment().WithMaxUnavailable(intstr.FromInt(0)))).
			WithSelector(applymetav1.LabelSelector().WithMatchLabels(map[string]string{"app.kubernetes.io/instance": "test-spicedb"})).
			WithTemplate(applycorev1.PodTemplateSpec().
				WithAnnotations(map[string]string{
					metadata.SpiceDBSecretRequirementsKey: "2",
					metadata.SpiceDBTargetMigrationKey:    "to-v1",
				}).
				WithLabels(map[string]string{
					metadata.KubernetesInstanceLabelKey:  "test-spicedb",
					metadata.KubernetesNameLabelKey:      "test-spicedb",
					metadata.KubernetesComponentLabelKey: metadata.ComponentSpiceDBLabelValue,
					metadata.KubernetesVersionLabelKey:   "v1",
				}).
				WithLabels(metadata.LabelsForComponent("test", metadata.ComponentSpiceDBLabelValue)).
				WithSpec(applycorev1.PodSpec().WithServiceAccountName("test").WithContainers(
					applycorev1.Container().WithName(ContainerNameSpiceDB).WithImage("image:v1").
						WithCommand("spicedb", "serve").
						WithEnv(
							applycorev1.EnvVar().WithName("SPICEDB_POD_NAME").WithValueFrom(applycorev1.EnvVarSource().WithFieldRef(applycorev1.ObjectFieldSelector().WithFieldPath("metadata.name"))),
							applycorev1.EnvVar().WithName("SPICEDB_LOG_LEVEL").WithValue("debug"),
							applycorev1.EnvVar().WithName("SPICEDB_GRPC_PRESHARED_KEY").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName("").WithKey("preshared_key"))),
							applycorev1.EnvVar().WithName("SPICEDB_DATASTORE_CONN_URI").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName("").WithKey("datastore_uri"))),
							applycorev1.EnvVar().WithName("SPICEDB_DISPATCH_UPSTREAM_ADDR").WithValue("kubernetes:///test.test:dispatch"),
							applycorev1.EnvVar().WithName("SPICEDB_DATASTORE_ENGINE").WithValue("cockroachdb"),
							applycorev1.EnvVar().WithName("SPICEDB_DISPATCH_CLUSTER_ENABLED").WithValue("true"),
							applycorev1.EnvVar().WithName("SPICEDB_TERMINATION_LOG_PATH").WithValue("/dev/termination-log"),
						).
						WithPorts(
							applycorev1.ContainerPort().WithContainerPort(50051).WithName("grpc"),
							applycorev1.ContainerPort().WithContainerPort(8443).WithName("gateway"),
							applycorev1.ContainerPort().WithContainerPort(9090).WithName("metrics"),
							applycorev1.ContainerPort().WithContainerPort(50053).WithName("dispatch"),
						).
						WithLivenessProbe(
							applycorev1.Probe().WithExec(applycorev1.ExecAction().WithCommand("grpc_health_probe", "-v", "-addr=localhost:50051")).
								WithInitialDelaySeconds(60).WithFailureThreshold(5).WithPeriodSeconds(10).WithTimeoutSeconds(5),
						).
						WithReadinessProbe(
							applycorev1.Probe().WithExec(applycorev1.ExecAction().WithCommand("grpc_health_probe", "-v", "-addr=localhost:50051")).
								WithFailureThreshold(5).WithPeriodSeconds(10).WithTimeoutSeconds(5),
						).
						WithVolumeMounts(
							applycorev1.VolumeMount().WithName(labelsVolume).WithMountPath("/etc/podlabels"),
							applycorev1.VolumeMount().WithName(annotationsVolume).WithMountPath("/etc/podannotations"),
						).
						WithTerminationMessagePolicy(corev1.TerminationMessageFallbackToLogsOnError),
				).WithVolumes(
					applycorev1.Volume().WithName(podNameVolume).
						WithDownwardAPI(applycorev1.DownwardAPIVolumeSource().WithItems(
							applycorev1.DownwardAPIVolumeFile().
								WithPath("name").
								WithFieldRef(applycorev1.ObjectFieldSelector().
									WithFieldPath("metadata.name"),
								),
						)),
					applycorev1.Volume().WithName(labelsVolume).
						WithDownwardAPI(applycorev1.DownwardAPIVolumeSource().WithItems(
							applycorev1.DownwardAPIVolumeFile().
								WithPath("labels").
								WithFieldRef(applycorev1.ObjectFieldSelector().
									WithFieldPath("metadata.labels"),
								),
						)),
					applycorev1.Volume().WithName(annotationsVolume).
						WithDownwardAPI(applycorev1.DownwardAPIVolumeSource().WithItems(
							applycorev1.DownwardAPIVolumeFile().
								WithPath("annotations").
								WithFieldRef(applycorev1.ObjectFieldSelector().
									WithFieldPath("metadata.annotations"),
								),
						)),
				))))
	for _, f := range apply {
		f(base)
	}
	return base
}

func expectedJob(apply ...func(dep *applybatchv1.JobApplyConfiguration)) *applybatchv1.JobApplyConfiguration {
	base := applybatchv1.Job("test-migrate-1", "test").
		WithLabels(metadata.LabelsForComponent("test", metadata.ComponentMigrationJobLabelValue)).
		WithLabels(map[string]string{
			metadata.KubernetesInstanceLabelKey:  "test",
			metadata.KubernetesNameLabelKey:      "test",
			metadata.KubernetesComponentLabelKey: metadata.ComponentSpiceDBLabelValue,
			metadata.KubernetesVersionLabelKey:   "v1",
		}).
		WithAnnotations(map[string]string{
			metadata.SpiceDBMigrationRequirementsKey: "1",
		}).
		WithOwnerReferences(applymetav1.OwnerReference().
			WithName("test").
			WithKind(v1alpha1.SpiceDBClusterKind).
			WithAPIVersion(v1alpha1.SchemeGroupVersion.String()).
			WithUID("1")).
		WithSpec(applybatchv1.JobSpec().WithTemplate(
			applycorev1.PodTemplateSpec().WithLabels(
				metadata.LabelsForComponent("test", metadata.ComponentMigrationJobLabelValue),
			).
				WithLabels(map[string]string{
					metadata.KubernetesInstanceLabelKey:  "test",
					metadata.KubernetesNameLabelKey:      "test",
					metadata.KubernetesComponentLabelKey: metadata.ComponentSpiceDBLabelValue,
					metadata.KubernetesVersionLabelKey:   "v1",
				}).
				WithSpec(applycorev1.PodSpec().WithServiceAccountName("test").
					WithContainers(
						applycorev1.Container().
							WithName("migrate").
							WithImage("image:v1").
							WithCommand("spicedb", "migrate", "to-v1").
							WithVolumeMounts(
								applycorev1.VolumeMount().WithName(labelsVolume).WithMountPath("/etc/podlabels"),
								applycorev1.VolumeMount().WithName(annotationsVolume).WithMountPath("/etc/podannotations"),
							).
							WithEnv(
								applycorev1.EnvVar().WithName("SPICEDB_LOG_LEVEL").WithValue("debug"),
								applycorev1.EnvVar().WithName("SPICEDB_DATASTORE_CONN_URI").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName("").WithKey("datastore_uri"))),
								applycorev1.EnvVar().WithName("SPICEDB_SECRETS").WithValueFrom(applycorev1.EnvVarSource().WithSecretKeyRef(applycorev1.SecretKeySelector().WithName("").WithKey("migration_secrets").WithOptional(true))),
								applycorev1.EnvVar().WithName("SPICEDB_DATASTORE_ENGINE").WithValue("cockroachdb"),
								applycorev1.EnvVar().WithName("SPICEDB_DISPATCH_CLUSTER_ENABLED").WithValue("true"),
								applycorev1.EnvVar().WithName("SPICEDB_TERMINATION_LOG_PATH").WithValue("/dev/termination-log"),
							).
							WithPorts(
								applycorev1.ContainerPort().WithContainerPort(50051).WithName("grpc"),
								applycorev1.ContainerPort().WithContainerPort(8443).WithName("gateway"),
								applycorev1.ContainerPort().WithContainerPort(9090).WithName("metrics"),
								applycorev1.ContainerPort().WithContainerPort(50053).WithName("dispatch"),
							).
							WithTerminationMessagePolicy(corev1.TerminationMessageFallbackToLogsOnError),
					).WithVolumes(
					applycorev1.Volume().WithName(podNameVolume).
						WithDownwardAPI(applycorev1.DownwardAPIVolumeSource().WithItems(
							applycorev1.DownwardAPIVolumeFile().
								WithPath("name").
								WithFieldRef(applycorev1.ObjectFieldSelector().
									WithFieldPath("metadata.name"),
								),
						)),
					applycorev1.Volume().WithName(labelsVolume).
						WithDownwardAPI(applycorev1.DownwardAPIVolumeSource().WithItems(
							applycorev1.DownwardAPIVolumeFile().
								WithPath("labels").
								WithFieldRef(applycorev1.ObjectFieldSelector().
									WithFieldPath("metadata.labels"),
								),
						)),
					applycorev1.Volume().WithName(annotationsVolume).
						WithDownwardAPI(applycorev1.DownwardAPIVolumeSource().WithItems(
							applycorev1.DownwardAPIVolumeFile().
								WithPath("annotations").
								WithFieldRef(applycorev1.ObjectFieldSelector().
									WithFieldPath("metadata.annotations"),
								),
						)),
				).WithRestartPolicy(corev1.RestartPolicyOnFailure))))
	for _, f := range apply {
		f(base)
	}
	return base
}

var testGlobalConfig = OperatorConfig{
	ImageName: "image",
	UpdateGraph: updates.UpdateGraph{
		Channels: []updates.Channel{
			{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
				Nodes: []updates.State{
					{ID: "v1", Tag: "v1", Migration: "to-v1"},
					{ID: "v1.0.0+test.v1", Tag: "v1.0.0+test.v1", Migration: "to-v1.0.0"},
					{ID: "long64charstring-4567890abcdef1234567890abcdef1234567890abcdef12", Tag: "long64charstring-4567890abcdef1234567890abcdef1234567890abcdef12", Migration: "to-long64charstring"},
				},
				Edges: map[string][]string{
					"v1":             {},
					"v1.0.0+test.v1": {"v1"},
					"long64charstring-4567890abcdef1234567890abcdef1234567890abcdef12": {"v1.0.0+test.v1"},
				},
			},
		},
	},
}

func TestRawConfigPop(t *testing.T) {
	tests := []struct {
		name     string
		config   RawConfig
		key      string
		expected string
	}{
		{
			name:     "returns empty string when key not found",
			config:   RawConfig{},
			key:      "nonexistent",
			expected: "",
		},
		{
			name:     "handles string values correctly",
			config:   RawConfig{"test": "value"},
			key:      "test",
			expected: "value",
		},
		{
			name:     "handles boolean true correctly",
			config:   RawConfig{"test": true},
			key:      "test",
			expected: "true",
		},
		{
			name:     "handles boolean false correctly",
			config:   RawConfig{"test": false},
			key:      "test",
			expected: "false",
		},
		{
			name:     "handles otelInsecure boolean true",
			config:   RawConfig{"otelInsecure": true},
			key:      "otelInsecure",
			expected: "true",
		},
		{
			name:     "handles otelInsecure string true",
			config:   RawConfig{"otelInsecure": "true"},
			key:      "otelInsecure",
			expected: "true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.config.Pop(tt.key)
			require.Equal(t, tt.expected, result)

			// Verify the key was removed from the config
			_, exists := tt.config[tt.key]
			require.False(t, exists, "key should be removed after Pop")
		})
	}
}
