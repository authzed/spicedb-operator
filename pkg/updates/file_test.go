package updates

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

func TestChannelForDatastore(t *testing.T) {
	graph := UpdateGraph{Channels: []Channel{
		{
			Name:     "postgres",
			Metadata: map[string]string{"datastore": "postgres"},
			Nodes:    []State{{ID: "v1.0.0"}},
		},
		{
			Name:     "cockroachdb",
			Metadata: map[string]string{"datastore": "cockroachdb"},
			Nodes:    []State{{ID: "v1.0.0"}},
		},
	}}

	t.Run("common case", func(t *testing.T) {
		channel, err := graph.ChannelForDatastore("cockroachdb")
		require.Nil(t, err)
		require.Equal(t, "cockroachdb", channel)

		channel, err = graph.ChannelForDatastore("postgres")
		require.Nil(t, err)
		require.Equal(t, "postgres", channel)
	})

	t.Run("case insensitive", func(t *testing.T) {
		channel, err := graph.ChannelForDatastore("POSTGRES")
		require.Nil(t, err)
		require.Equal(t, "postgres", channel)
	})
}

func TestAvailableVersions(t *testing.T) {
	table := []struct {
		name          string
		graph         *UpdateGraph
		engine        string
		version       v1alpha1.SpiceDBVersion
		expectedNames []string
		expectedErr   string
	}{
		{
			name:          "empty graph",
			graph:         &UpdateGraph{},
			engine:        "postgres",
			version:       v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "postgres"},
			expectedNames: nil,
			expectedErr:   `no source found for channel "postgres"`,
		},
		{
			name: "graph without matching channel",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Nodes:    []State{{ID: "v1.0.0"}},
			}}},
			engine:        "postgres",
			version:       v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "postgres"},
			expectedNames: nil,
			expectedErr:   `no source found for channel "postgres"`,
		},
		{
			name: "graph without edges",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:        "cockroachdb",
			version:       v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			expectedNames: nil,
			expectedErr:   "missing edges",
		},
		{
			name: "graph without nodes",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
			}}},
			engine:        "cockroachdb",
			version:       v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			expectedNames: nil,
			expectedErr:   "missing nodes",
		},
		{
			name: "simple patch update",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:        "cockroachdb",
			version:       v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			expectedNames: []string{"v1.0.1"},
		},
		{
			name: "head returns nothing",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:        "cockroachdb",
			version:       v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedNames: []string{},
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
			engine:        "cockroachdb",
			version:       v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedNames: []string{"v1.1.0"},
		},
	}

	for _, tt := range table {
		t.Run(tt.name, func(t *testing.T) {
			versions, err := tt.graph.AvailableVersions(tt.engine, tt.version)

			switch tt.expectedErr {
			case "":
				require.Nil(t, err)
			default:
				require.NotNil(t, err)
				require.Contains(t, err.Error(), tt.expectedErr)
			}

			if tt.expectedNames != nil {
				names := make([]string, 0)
				for _, v := range versions {
					names = append(names, v.Name)
				}
				require.Equal(t, tt.expectedNames, names)
			}
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
			name: "fallback to current version channel",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedTarget:    &v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedState:     State{ID: "v1.0.1"},
		},
		{
			name: "fallback to engine as channel",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			engine:            "cockroachdb",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0"},
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedTarget:    &v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedState:     State{ID: "v1.0.1"},
		},
		{
			name: "fail missing channel",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			channel:           "missing",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0"},
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedErr:       "no channel found",
		},
		{
			name: "rolling without current state fails",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			channel:           "cockroachdb",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			rolling:           true,
			expectedErr:       "no current state",
		},
		{
			name: "rolling uses current version",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			channel:           "cockroachdb",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			rolling:           true,
			expectedTarget:    &v1alpha1.SpiceDBVersion{Name: "v1.0.0", Channel: "cockroachdb"},
			expectedState:     State{ID: "v1.0.0"},
		},
		{
			name: "head returns same version",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			channel:           "cockroachdb",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			currentVersion:    &v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedTarget:    &v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedState:     State{ID: "v1.0.1"},
		},
		{
			name: "no version returns head",
			graph: &UpdateGraph{Channels: []Channel{{
				Name:     "cockroachdb",
				Metadata: map[string]string{"datastore": "cockroachdb"},
				Edges:    EdgeSet{"v1.0.0": {"v1.0.1"}},
				Nodes:    []State{{ID: "v1.0.1"}, {ID: "v1.0.0"}},
			}}},
			channel:           "cockroachdb",
			baseImage:         "ghcr.io/authzed/spicedb",
			expectedBaseImage: "ghcr.io/authzed/spicedb",
			expectedTarget:    &v1alpha1.SpiceDBVersion{Name: "v1.0.1", Channel: "cockroachdb"},
			expectedState:     State{ID: "v1.0.1"},
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
