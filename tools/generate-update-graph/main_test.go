package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerateEdges(t *testing.T) {
	tests := []struct {
		name  string
		nodes []versionNode
		want  map[string][]string
	}{
		{
			name: "single upgrade path",
			nodes: []versionNode{
				{id: "v1.0.0", sv: mustParseSemver("v1.0.0")},
				{id: "v1.1.0", sv: mustParseSemver("v1.1.0")},
			},
			want: map[string][]string{
				"v1.0.0": {"v1.1.0"},
			},
		},
		{
			name: "open upgrade path",
			nodes: []versionNode{
				{id: "v1.0.0", sv: mustParseSemver("v1.0.0")},
				{id: "v1.1.0", sv: mustParseSemver("v1.1.0")},
				{id: "v1.2.0", sv: mustParseSemver("v1.2.0")},
			},
			want: map[string][]string{
				"v1.0.0": {"v1.1.0", "v1.2.0"},
				"v1.1.0": {"v1.2.0"},
			},
		},
		{
			name: "deprecated version excluded from targets",
			nodes: []versionNode{
				{id: "v1.0.0", sv: mustParseSemver("v1.0.0")},
				{id: "v1.1.0", sv: mustParseSemver("v1.1.0"), deprecated: true},
				{id: "v1.2.0", sv: mustParseSemver("v1.2.0")},
			},
			want: map[string][]string{
				"v1.0.0": {"v1.2.0"},
				"v1.1.0": {"v1.2.0"},
			},
		},
		{
			name: "phase node acts as waypoint",
			nodes: []versionNode{
				{id: "v1.0.0", sv: mustParseSemver("v1.0.0")},
				{id: "v1.1.0-phase1", sv: mustParseSemver("v1.1.0-phase1"), phase: "write-old-read-new"},
				{id: "v1.1.0", sv: mustParseSemver("v1.1.0")},
				{id: "v1.2.0", sv: mustParseSemver("v1.2.0")},
			},
			want: map[string][]string{
				// v1.0.0 must stop at phase1 (can't skip past the waypoint)
				"v1.0.0":        {"v1.1.0-phase1"},
				"v1.1.0-phase1": {"v1.1.0", "v1.2.0"},
				"v1.1.0":        {"v1.2.0"},
			},
		},
		{
			name: "two-phase migration",
			nodes: []versionNode{
				{id: "v1.0.0", sv: mustParseSemver("v1.0.0")},
				{id: "v1.1.0-phase1", sv: mustParseSemver("v1.1.0-phase1"), phase: "phase1"},
				{id: "v1.1.0-phase2", sv: mustParseSemver("v1.1.0-phase2"), phase: "phase2"},
				{id: "v1.1.0", sv: mustParseSemver("v1.1.0")},
				{id: "v1.2.0", sv: mustParseSemver("v1.2.0")},
			},
			want: map[string][]string{
				"v1.0.0":        {"v1.1.0-phase1"},
				"v1.1.0-phase1": {"v1.1.0-phase2"},
				"v1.1.0-phase2": {"v1.1.0", "v1.2.0"},
				"v1.1.0":        {"v1.2.0"},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := generateEdges(tt.nodes)
			require.Equal(t, len(tt.want), len(got), "edge map length mismatch")
			for from, to := range tt.want {
				require.ElementsMatch(t, to, got[from], "edges from %s", from)
			}
		})
	}
}
