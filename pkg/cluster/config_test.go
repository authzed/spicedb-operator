package cluster

import "testing"

func TestToEnvVarName(t *testing.T) {
	tests := []struct {
		prefix string
		key    string
		want   string
	}{
		{
			"prefix",
			"key",
			"PREFIX_KEY",
		},
		{
			"SPICEDB",
			"grpcTLSKeyPath",
			"SPICEDB_GRPC_TLS_KEY_PATH",
		},
		{
			"SPICEDB",
			"grpcTlsKeyPath",
			"SPICEDB_GRPC_TLS_KEY_PATH",
		},
		{
			"SPICEDB",
			"dispatchUpstreamCAPath",
			"SPICEDB_DISPATCH_UPSTREAM_CA_PATH",
		},
	}
	for _, tt := range tests {
		t.Run(tt.prefix+"/"+tt.key, func(t *testing.T) {
			if got := ToEnvVarName(tt.prefix, tt.key); got != tt.want {
				t.Errorf("ToEnvVarName() = %v, want %v", got, tt.want)
			}
		})
	}
}
