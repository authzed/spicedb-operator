package updates

import (
	"cmp"
	"fmt"
	"strings"

	"github.com/samber/lo"
	"golang.org/x/exp/maps"
	"golang.org/x/exp/slices"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

const DatastoreMetadataKey = "datastore"

// Channel is a named series of updates in which we expect to have a path
// to the "head" of the channel from every node.
type Channel struct {
	// Name is the user-facing identifier for a graph of updates.
	Name string `json:"name"`

	// Metadata contains any additional properties about the channel.
	// For example, the applicable datastore is stored as metadata.
	Metadata map[string]string `json:"metadata,omitempty"`

	// Edges are the transitions between states in the update graph.
	Edges EdgeSet `json:"edges,omitempty"`

	// Nodes are the possible states in an update graph.
	Nodes []State `json:"nodes,omitempty"`
}

// EqualIdentity determines if two channels represent the same stream of
// updates by comparing their metadata.
func (c Channel) EqualIdentity(other Channel) bool {
	if c.Metadata == nil || other.Metadata == nil {
		return false
	}
	return c.Name == other.Name && c.Metadata[DatastoreMetadataKey] == other.Metadata[DatastoreMetadataKey]
}

// Clone makes a copy of a channel
func (c Channel) Clone() Channel {
	return Channel{
		Name:     c.Name,
		Metadata: maps.Clone(c.Metadata),
		Edges: lo.MapEntries(c.Edges, func(k string, v []string) (string, []string) {
			return k, slices.Clone(v)
		}),
		Nodes: slices.Clone(c.Nodes),
	}
}

// RemoveNodes nodes removes the specified nodes and any edges to or from those
// nodes.
func (c Channel) RemoveNodes(nodes []State) Channel {
	for _, n := range nodes {
		// remove edges to/from removed nodes
		delete(c.Edges, n.ID)
		for from, edges := range c.Edges {
			for i, to := range edges {
				if to == n.ID {
					c.Edges[from] = slices.Delete(c.Edges[from], i, i+1)
				}
			}
		}

		// remove node from node list
		idx := slices.Index(c.Nodes, n)
		if idx < 0 {
			continue
		}
		c.Nodes = slices.Delete(c.Nodes, idx, idx+1)
	}
	return c
}

// State is a "node" in the channel graph, indicating how to run at that
// release.
type State struct {
	ID        string `json:"id"`
	Tag       string `json:"tag,omitempty"`
	Migration string `json:"migration,omitempty"`
	Phase     string `json:"phase,omitempty"`
	Digest    string `json:"digest,omitempty"`

	// Deprecated releases can be updated from, but not to
	Deprecated bool `json:"-"`
}

// UpdateGraph holds a graph of required update edges
type UpdateGraph struct {
	Channels []Channel `json:"channels,omitempty"`
}

// DefaultChannelForDatastore returns the first channel for a specific datastore.
// This makes it possible to pick a channel even if a channel name is not
// provided. In the future we may want to explicitly define default channels.
func (g *UpdateGraph) DefaultChannelForDatastore(datastore string) (string, error) {
	for _, c := range g.Channels {
		if strings.EqualFold(c.Metadata[DatastoreMetadataKey], datastore) && strings.EqualFold(c.Metadata["default"], "true") {
			return c.Name, nil
		}
	}
	return "", fmt.Errorf("no channel found for datastore %q", datastore)
}

// SourceForChannel returns a channel represented as a Source for querying
func (g *UpdateGraph) SourceForChannel(engine, channel string) (Source, error) {
	for _, c := range g.Channels {
		if strings.EqualFold(c.Name, channel) && strings.EqualFold(c.Metadata["datastore"], engine) {
			return NewMemorySource(c.Nodes, c.Edges)
		}
	}
	return nil, fmt.Errorf("no channel for %q found with name %q", engine, channel)
}

// Copy returns a copy of the graph. The controller gets a copy so that
// the graph doesn't change during a single reconciliation.
func (g *UpdateGraph) Copy() UpdateGraph {
	return UpdateGraph{Channels: slices.Clone(g.Channels)}
}

// AvailableVersions traverses an UpdateGraph and collects a list of the
// safe versions for updating from the provided currentVersion.
func (g *UpdateGraph) AvailableVersions(engine string, v v1alpha1.SpiceDBVersion) ([]v1alpha1.SpiceDBVersion, error) {
	source, err := g.SourceForChannel(engine, v.Channel)
	if err != nil {
		return nil, fmt.Errorf("no source found for channel %q, can't compute available versions: %w", v.Channel, err)
	}

	availableVersions := make([]v1alpha1.SpiceDBVersion, 0)
	nextWithoutMigrations := source.NextVersionWithoutMigrations(v.Name)
	latest := source.LatestVersion(v.Name)
	if len(nextWithoutMigrations) > 0 {
		// TODO: should also account for downtime, i.e. dispatch api changes
		nextDirectVersion := v1alpha1.SpiceDBVersion{
			Name:        nextWithoutMigrations,
			Channel:     v.Channel,
			Attributes:  []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesNext},
			Description: "direct update with no migrations",
		}
		if nextWithoutMigrations == latest {
			nextDirectVersion.Description += ", head of channel"
			nextDirectVersion.Attributes = append(nextDirectVersion.Attributes, v1alpha1.SpiceDBVersionAttributesLatest)
		}
		availableVersions = append(availableVersions, nextDirectVersion)
	}

	next := source.NextVersion(v.Name)
	if len(next) > 0 && next != nextWithoutMigrations {
		nextVersion := v1alpha1.SpiceDBVersion{
			Name:        next,
			Channel:     v.Channel,
			Attributes:  []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesNext, v1alpha1.SpiceDBVersionAttributesMigration},
			Description: "update will run a migration",
		}
		if next == latest {
			nextVersion.Description += ", head of channel"
			nextVersion.Attributes = append(nextVersion.Attributes, v1alpha1.SpiceDBVersionAttributesLatest)
		}
		availableVersions = append(availableVersions, nextVersion)
	}
	if len(latest) > 0 && next != latest && nextWithoutMigrations != latest {
		availableVersions = append(availableVersions, v1alpha1.SpiceDBVersion{
			Name:        latest,
			Channel:     v.Channel,
			Attributes:  []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesLatest, v1alpha1.SpiceDBVersionAttributesMigration},
			Description: "head of the channel, multiple updates will run in sequence",
		})
	}

	// Check for options in other channels, but only show the safest update for
	// for each available channel.
	for _, c := range g.Channels {
		if c.Name == v.Channel {
			continue
		}
		if c.Metadata["datastore"] != engine {
			continue
		}
		source, err := g.SourceForChannel(engine, c.Name)
		if err != nil {
			continue
		}
		if next := source.NextVersionWithoutMigrations(v.Name); len(next) > 0 {
			availableVersions = append(availableVersions, v1alpha1.SpiceDBVersion{
				Name:        next,
				Channel:     c.Name,
				Attributes:  []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesNext},
				Description: "direct update with no migrations, different channel",
			})
			continue
		}
		if next := source.NextVersion(v.Name); len(next) > 0 {
			availableVersions = append(availableVersions, v1alpha1.SpiceDBVersion{
				Name:        next,
				Channel:     c.Name,
				Attributes:  []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesNext, v1alpha1.SpiceDBVersionAttributesMigration},
				Description: "update will run a migration, different channel",
			})
		}
	}

	return availableVersions, nil
}

func explodeImage(image string) (baseImage, tag, digest string) {
	imageMaybeTag, digest, hasDigest := strings.Cut(image, "@")
	if !hasDigest {
		digest = ""
	}
	baseImage, tag, hasTag := strings.Cut(imageMaybeTag, ":")
	if !hasTag {
		tag = ""
	}
	return
}

// ComputeTarget determines the target update version and state given an update
// graph and the proper context.
func (g *UpdateGraph) ComputeTarget(operatorImageName, clusterBaseImage, image, version, channel, engine string, currentVersion *v1alpha1.SpiceDBVersion, rolling bool) (baseImage string, target *v1alpha1.SpiceDBVersion, state State, err error) {
	explodedBaseImage, tag, digest := explodeImage(image)

	// If digest or tag are set in the image config, use that image as-is
	if len(digest) > 0 || len(tag) > 0 {
		state = State{Tag: tag, Digest: digest}
		baseImage = explodedBaseImage
		return
	}

	// The base image in the .spec.config.image field takes precedence over the
	// .spec.baseImage field, which takes precedence over the global base image.
	baseImage = cmp.Or(explodedBaseImage, clusterBaseImage, operatorImageName)
	if baseImage == "" {
		err = fmt.Errorf("no base image specified in cluster spec, startup flag, or operator config")
		return
	}

	// Fallback to the channel from the current currentVersion.
	if channel == "" && currentVersion != nil {
		channel = currentVersion.Channel
	}

	// If there's no still no channel, pick a default based on the engine.
	if channel == "" {
		channel, err = g.DefaultChannelForDatastore(engine)
		if err != nil {
			err = fmt.Errorf("couldn't find channel for datastore %q: %w", engine, err)
			return
		}
	}

	var updateSource Source
	if len(channel) > 0 {
		updateSource, err = g.SourceForChannel(engine, channel)
		if err != nil {
			err = fmt.Errorf("error fetching update source: %w", err)
			return
		}
	}

	target = &v1alpha1.SpiceDBVersion{}
	// Default to the currentVersion we're working towards.
	if currentVersion != nil {
		currentVersion.DeepCopyInto(target)
	}

	if len(channel) > 0 {
		target.Channel = channel
	}

	// If version is explicit, and there's no current version yet, just install
	if len(version) > 0 && (currentVersion == nil || len(currentVersion.Name) == 0) {
		state = updateSource.State(version)
		target.Name = state.ID
		target.Attributes = []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesMigration}
		return
	}

	// If version is explicit, and the explicit version matches the current
	// version, just install it
	if currentVersion != nil && currentVersion.Name == version && currentVersion.Channel == channel {
		state = updateSource.State(currentVersion.Name)
		return
	}

	var currentState State
	if currentVersion != nil {
		currentState = updateSource.State(currentVersion.Name)

		// If no current state, we're switching channels but the specified version
		// isn't there, so we're stuck on the current version for now.
		if len(currentState.ID) == 0 {
			var source Source
			source, err = g.SourceForChannel(engine, currentVersion.Channel)
			if err != nil {
				err = fmt.Errorf("error fetching update source: %w", err)
				return
			}
			currentState = source.State(currentVersion.Name)
			target.Channel = currentVersion.Channel
			target.Attributes = append(target.Attributes, v1alpha1.SpiceDBVersionAttributesNotInChannel)
		}
	}

	// If cluster is rolling, return the current state as reported by the status
	// and update graph.
	//
	// TODO(ecordell): This can change if the update graph is modified - do we
	// want to actually return status.image/etc?
	if rolling {
		if len(currentState.ID) == 0 {
			target = nil
			err = fmt.Errorf("cluster is rolling out, but no current state is defined")
			return
		}
		state = currentState
		return
	}

	// If currentVersion is set, we only use the subset of the update graph that leads
	// to that currentVersion.
	if currentVersion != nil && len(version) > 0 {
		updateSource, err = updateSource.Subgraph(version)
		if err != nil {
			err = fmt.Errorf("error finding update path from %s to %s", currentVersion.Name, version)
			return
		}
	}

	var targetVersion string
	if currentVersion != nil && len(currentVersion.Name) > 0 {
		targetVersion = updateSource.NextVersion(currentVersion.Name)
		if len(targetVersion) == 0 {
			// There's no next currentVersion, so use the current state.
			state = currentState
			return
		}
		if targetVersion != updateSource.NextVersionWithoutMigrations(currentVersion.Name) {
			target.Attributes = []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesMigration}
		}
	} else {
		// There's no current currentVersion, so install head.
		targetVersion = updateSource.LatestVersion("")
		target.Attributes = []v1alpha1.SpiceDBVersionAttributes{v1alpha1.SpiceDBVersionAttributesMigration}
	}

	// If we found the next step to take, return it.
	state = updateSource.State(targetVersion)
	target.Name = state.ID
	return
}

// Difference returns a graph that contains just edges in g that are not
// in the second update graph, plus the nodes/channels associated with them
// This is primarily used for diffing update graphs to know what edges require
// testing.
func (g *UpdateGraph) Difference(other *UpdateGraph) *UpdateGraph {
	diffGraph := &UpdateGraph{Channels: make([]Channel, 0)}

	// Find matching channels between the graphs
	for _, thisChannel := range g.Channels {
		foundMatchingChannel := false

		for _, otherChannel := range other.Channels {
			if thisChannel.EqualIdentity(otherChannel) {
				foundMatchingChannel = true
				// Determine which edges are in this channel but not the other
				keepEdges := make(map[string][]string, 0)

				for thisStartNode, thisEdgeSet := range thisChannel.Edges {
					// Keep all edges if the start node isn't in the other graph
					existingEdges, ok := otherChannel.Edges[thisStartNode]
					if !ok {
						keepEdges[thisStartNode] = thisEdgeSet
						continue
					}

					// Otherwise, only keep the edges in this channel not in
					// the other
					edges := sets.New(thisEdgeSet...).Difference(sets.New(existingEdges...))
					if edges.Len() > 0 {
						keepEdges[thisStartNode] = edges.UnsortedList()
					}
				}

				if len(keepEdges) == 0 {
					continue
				}

				// find all nodes that are referenced by some edge that we
				// are keeping in the new graph
				keepNodeIDs := sets.New(maps.Keys(keepEdges)...)
				for _, edgeset := range keepEdges {
					keepNodeIDs = keepNodeIDs.Union(sets.New(edgeset...))
				}
				keepNodes := make([]State, 0, len(keepNodeIDs))
				for _, id := range keepNodeIDs.UnsortedList() {
					idx := slices.IndexFunc(thisChannel.Nodes, func(state State) bool {
						return state.ID == id
					})
					if idx < 0 {
						panic(fmt.Sprintf("%s referenced in an edge for channel %s/%s, but not found in nodes for channel", id, thisChannel.Metadata[DatastoreMetadataKey], thisChannel.Name))
					}
					keepNodes = append(keepNodes, thisChannel.Nodes[idx])
				}

				diffGraph.Channels = append(diffGraph.Channels, Channel{
					Name:     thisChannel.Name,
					Metadata: thisChannel.Metadata,
					Edges:    keepEdges,
					Nodes:    keepNodes,
				})
			}
		}

		// if there's no matching channel, the whole channel is new
		if !foundMatchingChannel {
			diffGraph.Channels = append(diffGraph.Channels, thisChannel)
		}
	}
	return diffGraph
}
