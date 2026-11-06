package metadata

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/util/validation"
)

func TestBoundedName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		maxLength int
		expect    string
	}{
		{
			name:      "short name is unchanged",
			input:     "test-spicedb",
			maxLength: MaxGeneratedNameLength,
			expect:    "test-spicedb",
		},
		{
			name:      "name at the limit is unchanged",
			input:     strings.Repeat("a", MaxGeneratedNameLength),
			maxLength: MaxGeneratedNameLength,
			expect:    strings.Repeat("a", MaxGeneratedNameLength),
		},
		{
			name:      "long name is truncated and hashed",
			input:     strings.Repeat("a", MaxGeneratedNameLength) + "-spicedb",
			maxLength: MaxGeneratedNameLength,
			expect:    strings.Repeat("a", 54) + "-" + nameHash(strings.Repeat("a", MaxGeneratedNameLength)+"-spicedb"),
		},
		{
			name:      "trailing separators are trimmed before the hash",
			input:     "abcde-----fghij",
			maxLength: 14,
			expect:    "abcde-" + nameHash("abcde-----fghij"),
		},
		{
			name:      "limit leaves no room for a prefix",
			input:     strings.Repeat("a", 20),
			maxLength: nameHashLength + 1,
			expect:    nameHash(strings.Repeat("a", 20)),
		},
		{
			name:      "limit shorter than the hash",
			input:     strings.Repeat("a", 20),
			maxLength: 4,
			expect:    nameHash(strings.Repeat("a", 20))[:4],
		},
		{
			name:      "non-positive limit",
			input:     "test",
			maxLength: 0,
			expect:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BoundedName(tt.input, tt.maxLength)
			require.Equal(t, tt.expect, got)
			require.LessOrEqual(t, len(got), max(tt.maxLength, 0))
			if got != "" {
				require.Empty(t, validation.IsDNS1123Subdomain(got))
				require.Empty(t, validation.IsValidLabelValue(got))
			}
		})
	}
}

func TestBoundedNameIsDistinctAfterTruncation(t *testing.T) {
	prefix := strings.Repeat("a", MaxGeneratedNameLength)
	require.NotEqual(t,
		BoundedName(prefix+"-migrate-one", MaxGeneratedNameLength),
		BoundedName(prefix+"-migrate-two", MaxGeneratedNameLength),
	)
}
