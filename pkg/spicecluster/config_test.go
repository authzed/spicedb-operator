package spicecluster

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/errors"
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
		nn            types.NamespacedName
		uid           types.UID
		image         string
		allowedImages []string
		allowedTags   []string
		rawConfig     json.RawMessage
		secret        *corev1.Secret
	}
	tests := []struct {
		name         string
		args         args
		want         *Config
		wantWarnings []error
		wantErrs     []error
	}{
		{
			name: "missing required",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image"},
				rawConfig: json.RawMessage(`
					{
						"test": "field"
					}
				`),
			},
			wantErrs: []error{
				fmt.Errorf("datastoreEngine is a required field"),
				fmt.Errorf("secret must be provided"),
			},
			wantWarnings: []error{fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\"")},
		},
		{
			name: "simple",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image"},
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
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "set supported image",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image", "image2"},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"image": "image2"
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
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image2",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "set supported tag",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image", "other"},
				allowedTags:   []string{"tag", "tag2", "tag3@sha256:abc", "sha256:abcd"},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"image": "other:tag"
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
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "other:tag",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "set supported digest",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image", "other"},
				allowedTags:   []string{"tag", "tag2", "tag3@sha256:abc", "sha256:abcd"},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"image": "other@sha256:abc"
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
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "other@sha256:abc",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "set supported tagless digest",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image", "other"},
				allowedTags:   []string{"tag", "tag2", "tag3@sha256:abc", "sha256:abcd"},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"image": "other@sha256:abcd"
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
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "other@sha256:abcd",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "set an unsupported image",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image"},
				allowedTags:   []string{"tag"},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"image": "otherImage:tag"
					}
				`),
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{
				fmt.Errorf(`"otherImage:tag" invalid: "otherImage" is not in the configured list of allowed images`),
				fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\""),
			},
			want: &Config{
				MigrationConfig: MigrationConfig{
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "otherImage:tag",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "set an unsupported tag",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image"},
				allowedTags:   []string{"taggood", "taggood@sha256:abcd"},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"image": "image:tagbad"
					}
				`),
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{
				fmt.Errorf(`"image:tagbad" invalid: "tagbad" is not in the configured list of allowed tags`),
				fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\""),
			},
			want: &Config{
				MigrationConfig: MigrationConfig{
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image:tagbad",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "set an unsupported digest",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image"},
				allowedTags:   []string{"taggood", "taggood@sha256:abcd"},
				rawConfig: json.RawMessage(`
					{
						"datastoreEngine": "cockroachdb",
						"image": "image@sha256:1234"
					}
				`),
				secret: &corev1.Secret{Data: map[string][]byte{
					"datastore_uri": []byte("uri"),
					"preshared_key": []byte("psk"),
				}},
			},
			wantWarnings: []error{
				fmt.Errorf(`"image@sha256:1234" invalid: "sha256:1234" is not in the configured list of allowed digests`),
				fmt.Errorf("no TLS configured, consider setting \"tlsSecretName\""),
			},
			want: &Config{
				MigrationConfig: MigrationConfig{
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image@sha256:1234",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "set replicas as int",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image"},
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
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "set replicas as string",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image"},
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
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "set extra labels as string",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image"},
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
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "set extra labels as map",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image"},
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
					LogLevel:           "info",
					DatastoreEngine:    "cockroachdb",
					DatastoreURI:       "uri",
					TargetSpiceDBImage: "image",
					EnvPrefix:          "SPICEDB",
					SpiceDBCmd:         "spicedb",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "skip migrations bool",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image"},
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
					LogLevel:               "info",
					DatastoreEngine:        "cockroachdb",
					DatastoreURI:           "uri",
					SpannerCredsSecretRef:  "",
					TargetSpiceDBImage:     "image",
					EnvPrefix:              "SPICEDB",
					SpiceDBCmd:             "spicedb",
					DatastoreTLSSecretName: "",
				},
				SpiceConfig: SpiceConfig{
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
		},
		{
			name: "skip migrations string",
			args: args{
				nn:            types.NamespacedName{Namespace: "test", Name: "test"},
				uid:           types.UID("1"),
				image:         "image",
				allowedImages: []string{"image"},
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
					LogLevel:               "info",
					DatastoreEngine:        "cockroachdb",
					DatastoreURI:           "uri",
					SpannerCredsSecretRef:  "",
					TargetSpiceDBImage:     "image",
					EnvPrefix:              "SPICEDB",
					SpiceDBCmd:             "spicedb",
					DatastoreTLSSecretName: "",
				},
				SpiceConfig: SpiceConfig{
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
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, gotWarning, err := NewConfig(tt.args.nn, tt.args.uid, tt.args.image, tt.args.allowedImages, tt.args.allowedTags, tt.args.rawConfig, tt.args.secret)
			require.Equal(t, tt.want, got)
			require.EqualValues(t, errors.NewAggregate(tt.wantWarnings), gotWarning)
			require.EqualValues(t, errors.NewAggregate(tt.wantErrs), err)
		})
	}
}
