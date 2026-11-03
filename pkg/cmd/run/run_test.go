package run

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOptionsValidate(t *testing.T) {
	t.Parallel()

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
			t.Parallel()

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

func TestNewLogger(t *testing.T) {
	t.Parallel()

	t.Run("json format produces JSON logs", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		newLogger("json", &buf).Info("test message", "key", "value")

		var entry map[string]any
		require.NoError(t, json.Unmarshal(buf.Bytes(), &entry))
		require.Equal(t, "test message", entry["msg"])
		require.Equal(t, "value", entry["key"])
		require.Contains(t, entry["caller"], "run_test.go")
	})

	t.Run("json format samples repeated messages", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		logger := newLogger("json", &buf)
		for range 300 {
			logger.Info("repeated message")
		}

		lines := bytes.Count(buf.Bytes(), []byte("\n"))
		require.Positive(t, lines)
		require.Less(t, lines, 300)
	})

	t.Run("text format produces non-JSON logs", func(t *testing.T) {
		t.Parallel()

		var buf bytes.Buffer
		newLogger("text", &buf).Info("test message", "key", "value")

		require.Contains(t, buf.String(), "test message")
		require.False(t, json.Valid(bytes.TrimSpace(buf.Bytes())), "text output should not be JSON")
	})
}
