package updates

import (
	"fmt"

	"golang.org/x/exp/slices"
)

// Channel is a named series of updates in which we expect to have a path
// to the "head" of the channel from every node.
type Channel struct {
	Name     string            `json:"name"`
	Metadata map[string]string `json:"metadata,omitempty"`
	Edges    EdgeSet           `json:"edges,omitempty"`
	Nodes    []State           `json:"nodes,omitempty"`
}

// State is a "node" in the channel graph, indicating how to run at that
// release.
type State struct {
	ID        string `json:"id"`
	Tag       string `json:"tag,omitempty"`
	Migration string `json:"migration,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Digest    string `json:"digest,omitempty"`
}

// UpdateGraph holds a graph of required update edges
type UpdateGraph struct {
	Channels []Channel `json:"channels,omitempty"`
}

// ChannelForDatastore returns the first channel for a specific datastore.
// This makes it possible to pick a channel even if a channel name is not
// provided. In the future we may want to explicitly define default channels.
func (g *UpdateGraph) ChannelForDatastore(datastore string) (string, error) {
	for _, c := range g.Channels {
		if c.Metadata["datastore"] == datastore {
			return c.Name, nil
		}
	}
	return "", fmt.Errorf("no channel found for datastore %q", datastore)
}

// SourceForChannel returns a channel represented as a Source for querying
func (g *UpdateGraph) SourceForChannel(channel string) (Source, error) {
	for _, c := range g.Channels {
		if c.Name == channel {
			return NewMemorySource(c.Nodes, c.Edges)
		}
	}
	return nil, fmt.Errorf("no channel found with name %q", channel)
}

// Copy returns a copy of the graph. The controller gets a copy so that
// the graph doesn't change during a single reconciliation.
func (g *UpdateGraph) Copy() UpdateGraph {
	return UpdateGraph{Channels: slices.Clone(g.Channels)}
}
