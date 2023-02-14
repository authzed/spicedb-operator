package updates

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

func TestDefaultChannelForDatastore(t *testing.T) {
	graph := UpdateGraph{Channels: []Channel{
		{
			Name:     "postgres",
			Metadata: map[string]string{"datastore": "postgres", "default": "true"},
			Nodes:    []State{{ID: "v1.0.0"}},
		},
		{
			Name:     "cockroachdb",
			Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
			Nodes:    []State{{ID: "v1.0.0"}},
		},
	}}

	t.Run("common case", func(t *testing.T) {
		channel, err := graph.DefaultChannelForDatastore("cockroachdb")
		require.Nil(t, err)
		require.Equal(t, "cockroachdb", channel)

		channel, err = graph.DefaultChannelForDatastore("postgres")
		require.Nil(t, err)
		require.Equal(t, "postgres", channel)
	})

	t.Run("case insensitive", func(t *testing.T) {
		channel, err := graph.DefaultChannelForDatastore("POSTGRES")
		require.Nil(t, err)
		require.Equal(t, "postgres", channel)
	})
}

func TestAvailableVersions(t *testing.T) {
	table := []struct {
		name           string
		graph          *UpdateGraph
		engine         string
		currentVersion v1alpha1.SpiceDBVersion
		expected       []v1alpha1.SpiceDBVersion
		expectedErr    string
	}{
		{
			name:           "empty graph",
			graph:          &UpdateGraph{},
			engine:         "postgres",
			currentVersion: v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "postgres"},
			expectedErr:    `no source found for channel "postgres"`,
		},
		{
			name: "graph without matching channel",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Nodes:    []State{{ID: "v1.0.0"}},
			}}},
			engine:         "postgres",
			currentVersion: v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "postgres"},
			expectedErr:    `no source found for channel "postgres"`,
		},
		{
			name: "graph without edges",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:         "cockroachdb",
			currentVersion: v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			expectedErr:    "missing edges",
		},
		{
			name: "graph without nodes",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
			}}},
			engine:         "cockroachdb",
			currentVersion: v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			expectedErr:    "missing nodes",
		},
		{
			name: "simple patch update",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:         "cockroachdb",
			currentVersion: v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			expected:       []v1alpha1.SpiceDBVersion{{Name: "v1.0.1", Channel: "cockroachdb", Attributes: []v1alpha1.SpiceDBVersionAttributes{"next", "latest"}, Description: "direct update with no migrations, head of channel"}},
		},
		{
			name: "a next safe update, a next update with a migration, and a latest update with many steps are all available",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges: EdgeSet{
					"v1.0.0": {"v1.0.1", "v1.0.2"},
					"v1.0.1": {"v1.0.2"},
					"v1.0.2": {"v1.0.3"},
				},
				Nodes: []State{
					{ID: "v1.0.3", Migration: "b"},
					{ID: "v1.0.2", Migration: "a"},
					{ID: "v1.0.1"},
					{ID: "v1.0.0"},
				},
			}}},
			engine:         "cockroachdb",
			currentVersion: v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			expected: []v1alpha1.SpiceDBVersion{
				{Name: "v1.0.1", Channel: "cockroachdb", Attributes: []v1alpha1.SpiceDBVersionAttributes{"next"}, Description: "direct update with no migrations"},
				{Name: "v1.0.2", Channel: "cockroachdb", Attributes: []v1alpha1.SpiceDBVersionAttributes{"next", "migration"}, Description: "update will run a migration"},
				{Name: "v1.0.3", Channel: "cockroachdb", Attributes: []v1alpha1.SpiceDBVersionAttributes{"latest", "migration"}, Description: "head of the channel, multiple updates will run in sequence"},
			},
		},
		{
			name: "head returns nothing",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:         "cockroachdb",
			currentVersion: v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expected:       []v1alpha1.SpiceDBVersion{},
		},
		{
			name: "ignores old versions",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges: EdgeSet{
					"v1.0.0": {"v1.0.1", "v1.1.0"},
					"v1.0.1": {"v1.1.0"},
				},
				Nodes: []State{{ID: "v1.1.0"}, {ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:         "cockroachdb",
			currentVersion: v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expected:       []v1alpha1.SpiceDBVersion{{Name: "v1.1.0", Channel: "cockroachdb", Attributes: []v1alpha1.SpiceDBVersionAttributes{"next", "latest"}, Description: "direct update with no migrations, head of channel"}},
		},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			versions, err := tt.graph.AvailableVersions(tt.engine, tt.currentVersion)

			switch tt.expectedErr {
			case "":
				require.Nil(t, err)
			default:
				require.NotNil(t, err)
				require.Contains(t, err.Error(), tt.expectedErr)
			}

			require.EqualValues(t, tt.expected, versions)
		})
	}
}

func TestComputeTarget(t *testing.T) {
	table := []struct {
		name              string
		graph             *UpdateGraph
		baseImage         string
		image             string
		version           string
		channel           string
		engine            string
		currentVersion    *v1alpha1.SpiceDBVersion
		rolling           bool
		expectedBaseImage string
		expectedTarget    *v1alpha1.SpiceDBVersion
		expectedState     State
		expectedErr       string
	}{
		{
			name:        "missing images",
			graph:       &UpdateGraph{},
			expectedErr: "no base image",
		},
		{
			name: "image with tag returns tag",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			image:             "ghcr.io/authzed/spicedb:tag",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedState:     State{Tag: "tag"},
		},
		{
			name: "image without tag acts as base image",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			image:             "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedTarget:    &v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedState:     State{ID: "v1.0.1"},
		},
		{
			name: "fallback to current currentVersion channel",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedTarget:    &v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedState:     State{ID: "v1.0.1"},
		},
		{
			name: "fallback to default channel",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedTarget:    &v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedState:     State{ID: "v1.0.1"},
		},
		{
			name: "fail missing channel",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb", "default": "true"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			channel:           "missing",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0"},
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedErr:       "no channel",
		},
		{
			name: "rolling without current state fails",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			channel:           "cockroachdb",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			rolling:           true,
			expectedErr:       "no current state",
		},
		{
			name: "rolling uses current currentVersion",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			channel:           "cockroachdb",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			rolling:           true,
			expectedTarget:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			expectedState:     State{ID: "v1.0.0"},
		},
		{
			name: "crossing a migration boundary sets the migration attribute",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1", Migration: "b"}, {ID: "v1.0.0", Migration: "a"}},
			}}},
			engine:            "cockroachdb",
			channel:           "cockroachdb",
			version:           "v1.0.1",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			expectedTarget: &v1alpha1.SpiceDBVersion{
				Name:       "v1.0.1",
				Channel:    "cockroachdb",
				Attributes: []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesMigration},
			},
			expectedState: State{ID: "v1.0.1", Migration: "b"},
		},
		{
			name: "not crossing a migration boundary doesn't set the migration attribute",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1", Migration: "a"}, {ID: "v1.0.0", Migration: "a"}},
			}}},
			engine:            "cockroachdb",
			channel:           "cockroachdb",
			version:           "v1.0.1",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			expectedTarget: &v1alpha1.SpiceDBVersion{
				Name:    "v1.0.1",
				Channel: "cockroachdb",
			},
			expectedState: State{ID: "v1.0.1", Migration: "a"},
		},
		{
			name: "version specified and no current version",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			channel:           "cockroachdb",
			version:           "v1.0.0",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedTarget: &v1alpha1.SpiceDBVersion{
				Name:       "v1.0.0",
				Channel:    "cockroachdb",
				Attributes: []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesMigration},
			},
			expectedState: State{ID: "v1.0.0"},
		},
		{
			name: "version specified and current version is the same",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			channel:           "cockroachdb",
			version:           "v1.0.0",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedTarget: &v1alpha1.SpiceDBVersion{
				Name:       "v1.0.0",
				Channel:    "cockroachdb",
				Attributes: []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesMigration},
			},
			expectedState: State{ID: "v1.0.0"},
		},
		{
			name: "head returns same currentVersion",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			channel:           "cockroachdb",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedTarget:    &v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedState:     State{ID: "v1.0.1"},
		},
		{
			name: "no currentVersion returns head",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			channel:           "cockroachdb",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedTarget: &v1alpha1.SpiceDBVersion{
				Name:       "v1.0.1",
				Channel:    "cockroachdb",
				Attributes: []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesMigration},
			},
			expectedState: State{ID: "v1.0.1"},
		},
		{
			name: "no currentVersion returns spec.version if specified",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			channel:           "cockroachdb",
			version:           "v1.0.0",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedTarget: &v1alpha1.SpiceDBVersion{
				Name:       "v1.0.0",
				Channel:    "cockroachdb",
				Attributes: []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesMigration},
			},
			expectedState: State{ID: "v1.0.0"},
		},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			baseImage, target, state, err := tt.graph.ComputeTarget(
				tt.baseImage,
				tt.image,
				tt.version,
				tt.channel,
				tt.engine,
				tt.currentVersion,
				tt.rolling,
			)

			switch tt.expectedErr {
			case "":
				require.Nil(t, err)
			default:
				require.NotNil(t, err)
				require.Contains(t, err.Error(), tt.expectedErr)
			}

			require.Equal(t, tt.expectedBaseImage, baseImage)
			require.Equal(t, tt.expectedState, state)
			require.Equal(t, tt.expectedTarget, target)
		})
	}
}

func TestUpdateGraphDifference(t *testing.T) {
	tests := []struct {
		name                string
		first, second, want []Channel
	}{
		{
			name: "no difference",
			first: []Channel{{
				Name:     "test",
				Metadata: map[string]string{DatastoreMetadataKey: "test"},
				Nodes:    []State{{ID: "A", Tag: "A"}, {ID: "B", Tag: "B"}},
				Edges: map[string][]string{
					"A": {"B"},
				},
			}},
			second: []Channel{{
				Name:     "test",
				Metadata: map[string]string{DatastoreMetadataKey: "test"},
				Nodes:    []State{{ID: "A", Tag: "A"}, {ID: "B", Tag: "B"}},
				Edges: map[string][]string{
					"A": {"B"},
				},
			}},
			want: []Channel{},
		},
		{
			name: "new channel",
			first: []Channel{{
				Name:     "test",
				Metadata: map[string]string{DatastoreMetadataKey: "test"},
				Nodes:    []State{{ID: "A", Tag: "A"}, {ID: "B", Tag: "B"}},
				Edges: map[string][]string{
					"A": {"B"},
				},
			}},
			second: []Channel{},
			want: []Channel{{
				Name:     "test",
				Metadata: map[string]string{DatastoreMetadataKey: "test"},
				Nodes:    []State{{ID: "A", Tag: "A"}, {ID: "B", Tag: "B"}},
				Edges: map[string][]string{
					"A": {"B"},
				},
			}},
		},
		{
			name: "channel has new nodes",
			first: []Channel{{
				Name:     "test",
				Metadata: map[string]string{DatastoreMetadataKey: "test"},
				Nodes:    []State{{ID: "A", Tag: "A"}, {ID: "B", Tag: "B"}, {ID: "C", Tag: "C"}},
				Edges: map[string][]string{
					"A": {"B", "C"},
					"B": {"C"},
				},
			}},
			second: []Channel{{
				Name:     "test",
				Metadata: map[string]string{DatastoreMetadataKey: "test"},
				Nodes:    []State{{ID: "A", Tag: "A"}, {ID: "B", Tag: "B"}},
				Edges: map[string][]string{
					"A": {"B"},
				},
			}},
			want: []Channel{{
				Name:     "test",
				Metadata: map[string]string{DatastoreMetadataKey: "test"},
				Nodes:    []State{{ID: "A", Tag: "A"}, {ID: "B", Tag: "B"}, {ID: "C", Tag: "C"}},
				Edges: map[string][]string{
					"A": {"C"},
					"B": {"C"},
				},
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &UpdateGraph{
				Channels: tt.first,
			}
			got := g.Difference(&UpdateGraph{
				Channels: tt.second,
			})
			GraphEqual(t, got, &UpdateGraph{Channels: tt.want})
		})
	}
}

func GraphEqual(t testing.TB, a, b *UpdateGraph) {
	require.Equal(t, len(a.Channels), len(b.Channels))

	equalCount := 0
	for _, ac := range a.Channels {
		for _, bc := range b.Channels {
			if ac.EqualIdentity(bc) {
				equalCount++
				require.ElementsMatch(t, ac.Nodes, bc.Nodes)
				for source, edges := range ac.Edges {
					require.ElementsMatch(t, edges, bc.Edges[source])
				}
				for source, edges := range bc.Edges {
					require.ElementsMatch(t, edges, ac.Edges[source])
				}
			}
		}
	}
	require.Equal(t, len(a.Channels), equalCount)
}
