package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStringKey(t *testing.T) {
	t.Parallel()

	for _, val := range []struct {
		description string
		value       any
		expected    any
	}{
		{"returns default when absent", nil, ""},
		{"returns value when present", "value", "value"},
		{"silently ignores unexpected type and returns default", 1, ""},
	} {
		t.Run(val.description, func(t *testing.T) {
			t.Parallel()

			sk := newStringKey("test")
			config := RawConfig{}
			if val.value != nil {
				config = RawConfig{"test": val.value}
			}
			require.Equal(t, val.expected, sk.pop(config))
			if val.value != nil {
				require.Empty(t, config)
			}
		})
	}
}

func TestBoolOrStringKey(t *testing.T) {
	t.Parallel()

	for _, val := range []struct {
		description string
		value       any
		def         bool
		expected    any
		err         bool
	}{
		{"returns true as default when absent", nil, true, true, false},
		{"returns false as default when absent", nil, false, false, false},
		{"returns true when present", true, false, true, false},
		{"returns false when present", false, true, false, false},
		{"returns parsed true string", "true", false, true, false},
		{"returns parsed false string", "false", true, false, false},
		{"fails when invalid string", "", true, false, true},
		{"fails with unexpected type", int64(1), true, false, true},
	} {
		t.Run(val.description, func(t *testing.T) {
			t.Parallel()

			sk := newBoolOrStringKey("test", val.def)
			config := RawConfig{}
			if val.value != nil {
				config = RawConfig{"test": val.value}
			}
			result, err := sk.pop(config)
			if val.value != nil {
				require.Empty(t, config)
			}
			if val.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, val.expected, result)
			}
		})
	}
}

func TestIntOrStringKey(t *testing.T) {
	t.Parallel()

	for _, val := range []struct {
		description string
		value       any
		def         int64
		expected    any
		err         bool
	}{
		{"returns default when absent", nil, 1, int64(1), false},
		{"returns parsed value of string", "10", 1, int64(10), false},
		{"returns int64 from float", float64(10), 1, int64(10), false},
		{"fails when invalid string", "", 1, int64(1), true},
		{"fails when unexpected type", struct{}{}, 1, int64(1), true},
	} {
		t.Run(val.description, func(t *testing.T) {
			t.Parallel()

			sk := newIntOrStringKey("test", val.def)
			config := RawConfig{}
			if val.value != nil {
				config = RawConfig{"test": val.value}
			}
			result, err := sk.pop(config)
			if val.value != nil {
				require.Empty(t, config)
			}
			if val.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, val.expected, result)
			}
		})
	}
}

func TestMetadataSetKey(t *testing.T) {
	t.Parallel()

	input := map[string]any{"k": "v", "k2": "v2"}
	invalidInput := map[string]any{"k": 1, "k2": "v2"}
	empty := map[string]string{}
	notEmpty := map[string]string{"k": "v", "k2": "v2"}
	for _, val := range []struct {
		description   string
		value         any
		def           map[string]string
		expected      map[string]string
		expectedWarns bool
		err           bool
	}{
		{"parses pod label string as map", "k=v,k2=v2", empty, notEmpty, false, false},
		{"returns empty map when no labels", "", empty, empty, false, false},
		{"returns empty map when not present", nil, empty, nil, false, false},
		{"supports map as input", input, empty, notEmpty, false, false},
		{"fails on unexpected type", struct{}{}, empty, empty, false, true},
		{"recovers and warns on partially valid string", "k=v,k2", empty, map[string]string{"k": "v"}, true, false},
		{"recovers and warns on invalid map value", invalidInput, empty, map[string]string{"k2": "v2"}, true, false},
	} {
		t.Run(val.description, func(t *testing.T) {
			t.Parallel()

			k := metadataSetKey("test")
			config := RawConfig{}
			if val.value != nil {
				config = RawConfig{"test": val.value}
			}
			result, warns, err := k.pop(config, "pod", "metadata")
			if val.value != nil {
				require.Empty(t, config)
			}
			if val.expectedWarns {
				require.NotEmpty(t, warns)
			} else {
				require.Empty(t, warns)
			}
			if val.err {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, val.expected, result)
			}
		})
	}
}
