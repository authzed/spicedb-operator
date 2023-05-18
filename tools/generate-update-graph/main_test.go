package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/authzed/spicedb-operator/pkg/updates"
)

func TestEdgesFromPatterns(t *testing.T) {
	tests := []struct {
		name     string
		patterns map[string]string
		releases []updates.State
		want     map[string][]string
	}{
		{
			name: "single edge",
			releases: []updates.State{
				{ID: "v1.0.0"},
				{ID: "v1.1.0"},
			},
			patterns: map[string]string{
				"v1.0.0": "1.1.0",
			},
			want: map[string][]string{
				"v1.0.0": {"v1.1.0"},
			},
		},
		{
			name: "open edge range",
			releases: []updates.State{
				{ID: "v1.0.0"},
				{ID: "v1.1.0"},
				{ID: "v1.2.0"},
			},
			patterns: map[string]string{
				"v1.0.0": ">1.0.0",
			},
			want: map[string][]string{
				"v1.0.0": {"v1.1.0", "v1.2.0"},
			},
		},
		{
			name: "closed edge range",
			releases: []updates.State{
				{ID: "v1.0.0"},
				{ID: "v1.1.0"},
				{ID: "v1.2.0"},
			},
			patterns: map[string]string{
				"v1.0.0": ">1.0.0 <=1.1.0",
			},
			want: map[string][]string{
				"v1.0.0": {"v1.1.0"},
			},
		},
		{
			name: "edge range with deprecated release in range",
			releases: []updates.State{
				{ID: "v1.0.0"},
				{ID: "v1.1.0", Deprecated: true},
				{ID: "v1.2.0"},
			},
			patterns: map[string]string{
				"v1.0.0": ">1.0.0",
			},
			want: map[string][]string{
				"v1.0.0": {"v1.2.0"},
			},
		},
		{
			name: "edge range with omitted version",
			releases: []updates.State{
				{ID: "v1.0.0"},
				{ID: "v1.1.0"},
				{ID: "v1.2.0"},
				{ID: "v1.3.0"},
			},
			patterns: map[string]string{
				"v1.0.0": ">1.0.0 !1.2.0",
			},
			want: map[string][]string{
				"v1.0.0": {"v1.1.0", "v1.3.0"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := edgesFromPatterns(tt.patterns, tt.releases)
			for from, to := range tt.want {
				require.ElementsMatch(t, to, got[from])
			}
			for from, to := range got {
				require.ElementsMatch(t, to, tt.want[from])
			}
		})
	}
}
