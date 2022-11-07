package updates

import (
	"fmt"

	"golang.org/x/exp/slices"
)

type Channel struct {
	Name     string
	Metadata map[string]string `json:"metadata,omitempty"`
	Edges    EdgeSet           `json:"edges,omitempty"`
	Nodes    []State           `json:"nodes,omitempty"`
}

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

func (g *UpdateGraph) SourceForDatastore(datastore string) (Source, error) {
	for _, c := range g.Channels {
		if c.Metadata["datastore"] == datastore {
			return NewMemorySource(c.Nodes, c.Edges)
		}
	}
	return nil, fmt.Errorf("no channel found for datastore %q", datastore)
}

func (g *UpdateGraph) SourceForChannel(channel string) (Source, error) {
	for _, c := range g.Channels {
		if c.Name == channel {
			return NewMemorySource(c.Nodes, c.Edges)
		}
	}
	return nil, fmt.Errorf("no channel found with name %q", channel)
}

func (g *UpdateGraph) Copy() UpdateGraph {
	return UpdateGraph{Channels: slices.Clone(g.Channels)}
}
