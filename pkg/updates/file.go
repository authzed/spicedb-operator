package updates

import (
	"fmt"
	"strings"

	"github.com/jzelinskie/stringz"
	"golang.org/x/exp/slices"

	"github.com/authzed/spicedb-operator/pkg/apis/authzed/v1alpha1"
)

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

// DefaultChannelForDatastore returns the first channel for a specific datastore.
// This makes it possible to pick a channel even if a channel name is not
// provided. In the future we may want to explicitly define default channels.
func (g *UpdateGraph) DefaultChannelForDatastore(datastore string) (string, error) {
	for _, c := range g.Channels {
		if strings.EqualFold(c.Metadata["datastore"], datastore) && strings.EqualFold(c.Metadata["default"], "true") {
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

// ComputeTarget determines the target update currentVersion and state given an update
// graph and the proper context.
func (g *UpdateGraph) ComputeTarget(defaultBaseImage, image, version, channel, engine string, currentVersion *v1alpha1.SpiceDBVersion, rolling bool) (baseImage string, target *v1alpha1.SpiceDBVersion, state State, err error) {
	baseImage, tag, digest := explodeImage(image)

	// If digest or tag are set, don't use an update graph.
	if len(digest) > 0 || len(tag) > 0 {
		state = State{Tag: tag, Digest: digest}
		return
	}

	// Fallback to the default base image if none is set.
	baseImage = stringz.DefaultEmpty(baseImage, defaultBaseImage)
	if baseImage == "" {
		err = fmt.Errorf("no base image in operator config, and none specified in image")
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

	// Default to the currentVersion we're working toward.
	target = currentVersion

	var currentState State
	if currentVersion != nil {
		currentState = updateSource.State(currentVersion.Name)
	}

	// If cluster is rolling, return the current state as reported by the status
	// and update graph.
	//
	// TODO(ecordell): This can change if the update graph is modified - do we
	// want to actually return status.image/etc?
	if rolling {
		if len(currentState.ID) == 0 {
			err = fmt.Errorf("cluster is rolling out, but no current state is defined")
			return
		}
		state = currentState
		return
	}

	// If currentVersion is set, we only use the subset of the update graph that leads
	// to that currentVersion.
	if len(version) > 0 {
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
			target = currentVersion
			return
		}
	} else {
		// There's no current currentVersion, so install head.
		// TODO(jzelinskie): find a way to make this less "magical"
		targetVersion = updateSource.LatestVersion("")
	}

	// If we found the next step to take, return it.
	state = updateSource.State(targetVersion)
	target = &v1alpha1.SpiceDBVersion{
		Name:    state.ID,
		Channel: channel,
	}
	return
}
