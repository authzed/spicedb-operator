package updates

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMemorySource(t *testing.T) {
	type want struct {
		latest, next, nextDirect string
	}
	tests := []struct {
		name                        string
		OrderedNodes                []State
		Edges                       EdgeSet
		expectedForSubgraphWithHead map[string]map[string]want
		expectedForID               map[string]want
		newErr                      string
	}{
		{
			name: "not all entries have paths to head",
			OrderedNodes: []State{
				{ID: "v4", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v3", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v2", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v1", Tag: "tag", Migration: "migration", Phase: ""},
			},
			Edges: map[string][]string{
				"v1": {"v2", "v3", "v4"},
				"v2": {"v3", "v4"},
			},
			newErr: "there is no path from v3 to v4",
		},
		{
			name: "cycle in channel",
			OrderedNodes: []State{
				{ID: "v4", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v3", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v2", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v1", Tag: "tag", Migration: "migration", Phase: ""},
			},
			Edges: map[string][]string{
				"v1": {"v2", "v3"},
				"v2": {"v3"},
				"v3": {"v2"},
			},
			newErr: "channel cycle detected",
		},
		{
			name: "latest is v4, no required steps",
			OrderedNodes: []State{
				{ID: "v4", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v3", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v2", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v1", Tag: "tag", Migration: "migration", Phase: ""},
			},
			Edges: map[string][]string{
				"v1": {"v2", "v3", "v4"},
				"v2": {"v3", "v4"},
				"v3": {"v4"},
			},
			expectedForID: map[string]want{
				"v1": {next: "v4", nextDirect: "v4", latest: "v4"},
				"v2": {next: "v4", nextDirect: "v4", latest: "v4"},
				"v3": {next: "v4", nextDirect: "v4", latest: "v4"},
				"":   {next: "", nextDirect: "", latest: "v4"},
			},
			expectedForSubgraphWithHead: map[string]map[string]want{
				"v3": {
					"v1": {next: "v3", nextDirect: "v3", latest: "v3"},
					"v2": {next: "v3", nextDirect: "v3", latest: "v3"},
					"v3": {next: "", nextDirect: "", latest: ""},
					"":   {next: "", nextDirect: "", latest: "v3"},
				},
				"v2": {
					"v1": {next: "v2", nextDirect: "v2", latest: "v2"},
					"v2": {next: "", nextDirect: "", latest: ""},
					"":   {next: "", nextDirect: "", latest: "v2"},
				},
				"v1": {
					"v1": {next: "", nextDirect: "", latest: ""},
					"":   {next: "", nextDirect: "", latest: "v1"},
				},
			},
		},
		{
			name: "latest is v4, required step at v3",
			OrderedNodes: []State{
				{ID: "v4", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v3", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v2", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v1", Tag: "tag", Migration: "migration", Phase: ""},
			},
			Edges: map[string][]string{
				"v1": {"v2", "v3"},
				"v2": {"v3"},
				"v3": {"v4"},
			},
			expectedForID: map[string]want{
				"v1": {next: "v3", nextDirect: "v3", latest: "v4"},
				"v2": {next: "v3", nextDirect: "v3", latest: "v4"},
				"v3": {next: "v4", nextDirect: "v4", latest: "v4"},
			},
			expectedForSubgraphWithHead: map[string]map[string]want{
				"v3": {
					"v1": {next: "v3", nextDirect: "v3", latest: "v3"},
					"v2": {next: "v3", nextDirect: "v3", latest: "v3"},
					"v3": {next: "", nextDirect: "", latest: ""},
					"":   {next: "", nextDirect: "", latest: "v3"},
				},
				"v2": {
					"v1": {next: "v2", nextDirect: "v2", latest: "v2"},
					"v2": {next: "", nextDirect: "", latest: ""},
					"":   {next: "", nextDirect: "", latest: "v2"},
				},
				"v1": {
					"v1": {next: "", nextDirect: "", latest: ""},
					"":   {next: "", nextDirect: "", latest: "v1"},
				},
			},
		},
		{
			name: "latest is v4, multiple required steps",
			OrderedNodes: []State{
				{ID: "v4", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v3", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v2", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v1", Tag: "tag", Migration: "migration", Phase: ""},
			},
			Edges: map[string][]string{
				"v1": {"v2"},
				"v2": {"v3"},
				"v3": {"v4"},
			},
			expectedForID: map[string]want{
				"v1": {next: "v2", nextDirect: "v2", latest: "v4"},
				"v2": {next: "v3", nextDirect: "v3", latest: "v4"},
				"v3": {next: "v4", nextDirect: "v4", latest: "v4"},
			},
			expectedForSubgraphWithHead: map[string]map[string]want{
				"v3": {
					"v1": {next: "v2", nextDirect: "v2", latest: "v3"},
					"v2": {next: "v3", nextDirect: "v3", latest: "v3"},
					"v3": {next: "", nextDirect: "", latest: ""},
				},
				"v2": {
					"v1": {next: "v2", nextDirect: "v2", latest: "v2"},
					"v2": {next: "", nextDirect: "", latest: ""},
				},
				"v1": {
					"v1": {next: "", nextDirect: "", latest: ""},
				},
			},
		},
		{
			name: "required migration in list",
			OrderedNodes: []State{
				{ID: "v4", Tag: "tag", Migration: "migration3", Phase: "phase3"},
				{ID: "v3", Tag: "tag", Migration: "migration2", Phase: "phase2"},
				{ID: "v2", Tag: "tag", Migration: "migration2", Phase: "phase2"},
				{ID: "v1", Tag: "tag", Migration: "migration", Phase: ""},
			},
			Edges: map[string][]string{
				"v1": {"v2"},
				"v2": {"v3"},
				"v3": {"v4"},
			},
			expectedForID: map[string]want{
				"v1": {next: "v2", nextDirect: "", latest: "v4"},
				"v2": {next: "v3", nextDirect: "v3", latest: "v4"},
				"v3": {next: "v4", nextDirect: "", latest: "v4"},
				"v4": {next: "", nextDirect: "", latest: ""},
			},
			expectedForSubgraphWithHead: map[string]map[string]want{
				"v3": {
					"v1": {next: "v2", nextDirect: "", latest: "v3"},
					"v2": {next: "v3", nextDirect: "v3", latest: "v3"},
					"v3": {next: "", nextDirect: "", latest: ""},
				},
				"v2": {
					"v1": {next: "v2", nextDirect: "", latest: "v2"},
					"v2": {next: "", nextDirect: "", latest: ""},
				},
				"v1": {
					"v1": {next: "", nextDirect: "", latest: ""},
				},
			},
		},
		{
			name: "latest is v5, required step at v4, migration at v3",
			OrderedNodes: []State{
				{ID: "v5", Tag: "tag", Migration: "migration2", Phase: ""},
				{ID: "v4", Tag: "tag", Migration: "migration2", Phase: "phase2"},
				{ID: "v3", Tag: "tag", Migration: "migration2", Phase: "phase1"},
				{ID: "v2", Tag: "tag", Migration: "migration", Phase: ""},
				{ID: "v1", Tag: "tag", Migration: "migration", Phase: ""},
			},
			Edges: map[string][]string{
				"v1": {"v2", "v3", "v4"},
				"v2": {"v3", "v4"},
				"v3": {"v4"},
				"v4": {"v5"},
			},
			expectedForID: map[string]want{
				"v1": {next: "v4", nextDirect: "v2", latest: "v5"},
				"v2": {next: "v4", nextDirect: "", latest: "v5"},
				"v3": {next: "v4", nextDirect: "", latest: "v5"},
			},
			expectedForSubgraphWithHead: map[string]map[string]want{
				"v4": {
					"v1": {next: "v4", nextDirect: "v2", latest: "v4"},
					"v2": {next: "v4", nextDirect: "", latest: "v4"},
					"v3": {next: "v4", nextDirect: "", latest: "v4"},
					"v4": {next: "", nextDirect: "", latest: ""},
				},
				"v3": {
					"v1": {next: "v3", nextDirect: "v2", latest: "v3"},
					"v2": {next: "v3", nextDirect: "", latest: "v3"},
					"v3": {next: "", nextDirect: "", latest: ""},
				},
				"v2": {
					"v1": {next: "v2", nextDirect: "v2", latest: "v2"},
					"v2": {next: "", nextDirect: "", latest: ""},
				},
				"v1": {
					"v1": {next: "", nextDirect: "", latest: ""},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := NewMemorySource(tt.OrderedNodes, tt.Edges)
			if err != nil {
				require.Contains(t, err.Error(), tt.newErr)
			}
			for id, want := range tt.expectedForID {
				t.Run(fmt.Sprintf("NextVersion(%s)", id), func(t *testing.T) {
					require.Equal(t, want.next, m.NextVersion(id))
				})
				t.Run(fmt.Sprintf("NextVersionWithoutMigrations(%s)", id), func(t *testing.T) {
					require.Equal(t, want.nextDirect, m.NextVersionWithoutMigrations(id))
				})
				t.Run(fmt.Sprintf("LatestVersion(%s)", id), func(t *testing.T) {
					require.Equal(t, want.latest, m.LatestVersion(id))
				})
			}
			for newHead, expected := range tt.expectedForSubgraphWithHead {
				s, err := m.Subgraph(newHead)
				require.NoError(t, err)
				for id, want := range expected {
					t.Run(fmt.Sprintf("head=%s,NextVersion(%s)", newHead, id), func(t *testing.T) {
						require.Equal(t, want.next, s.NextVersion(id))
					})
					t.Run(fmt.Sprintf("head=%s,NextVersionWithoutMigrations(%s)", newHead, id), func(t *testing.T) {
						require.Equal(t, want.nextDirect, s.NextVersionWithoutMigrations(id))
					})
					t.Run(fmt.Sprintf("head=%s,LatestVersion(%s)", newHead, id), func(t *testing.T) {
						require.Equal(t, want.latest, s.LatestVersion(id))
					})
				}
			}
		})
	}
}

func TestMemorySourceState(t *testing.T) {
	type fields struct {
		OrderedNodes []State
		Edges        EdgeSet
	}
	tests := []struct {
		name    string
		fields  fields
		id      string
		want    State
		wantErr string
	}{
		{
			name: "no nodes",
			fields: fields{
				Edges: map[string][]string{
					"1": {"2"},
				},
			},
			wantErr: "missing nodes",
		},
		{
			name: "no edges",
			fields: fields{
				OrderedNodes: []State{
					{ID: "1"},
				},
			},
			wantErr: "missing edges",
		},
		{
			name: "edge not in list",
			fields: fields{
				OrderedNodes: []State{
					{ID: "2"},
					{ID: "1"},
				},
				Edges: map[string][]string{
					"1": {"3"},
				},
			},
			wantErr: "node list is missing node 3",
		},
		{
			name: "found",
			fields: fields{
				OrderedNodes: []State{
					{ID: "2"},
					{ID: "1"},
				},
				Edges: map[string][]string{
					"1": {"2"},
				},
			},
			id:   "1",
			want: State{ID: "1"},
		},
		{
			name: "missing",
			fields: fields{
				OrderedNodes: []State{
					{ID: "2"},
					{ID: "1"},
				},
				Edges: map[string][]string{
					"1": {"2"},
				},
			},
			id: "3",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := NewMemorySource(tt.fields.OrderedNodes, tt.fields.Edges)
			if err != nil {
				require.EqualError(t, err, tt.wantErr)
				return
			}
			require.Equal(t, tt.want, m.State(tt.id))
		})
	}
}
