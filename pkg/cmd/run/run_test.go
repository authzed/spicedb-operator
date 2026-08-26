package run

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOptionsValidate(t *testing.T) {
	tests := []struct {
		name      string
		options   *Options
		wantError bool
	}{
		{
			name: "valid text format",
			options: &Options{
				LogFormat: "text",
			},
			wantError: false,
		},
		{
			name: "valid json format",
			options: &Options{
				LogFormat: "json",
			},
			wantError: false,
		},
		{
			name: "invalid format",
			options: &Options{
				LogFormat: "yaml",
			},
			wantError: true,
		},
		{
			name: "empty format defaults to text",
			options: &Options{
				LogFormat: "",
			},
			wantError: false, // Empty should use default "text" without error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set default debug flags to avoid nil pointer
			tt.options.DebugFlags = RecommendedOptions().DebugFlags

			err := tt.options.Validate()
			if tt.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
