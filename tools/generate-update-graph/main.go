package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/blang/semver/v4"
	"sigs.k8s.io/yaml"

	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/updates"
)

//go:generate go run . ../../magefiles/versions.yaml ../../proposed-update-graph.yaml

// DatastoreConfig holds per-datastore configuration for a version.
type DatastoreConfig struct {
	Migration  string
	Phase      string
	Deprecated bool
	Waypoint   bool
}

// Version is a single entry in the versions YAML file.
type Version struct {
	ID     string
	Tag    string
	Config map[string]DatastoreConfig
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: generate-update-graph <versions-yaml> <output-yaml>")
		os.Exit(1)
	}

	versions, err := readVersions(os.Args[1])
	if err != nil {
		panic(err)
	}

	opconfig := generateGraph(versions)

	out, err := yaml.Marshal(&opconfig)
	if err != nil {
		panic(err)
	}

	if err := os.WriteFile(os.Args[2], out, 0o600); err != nil {
		panic(err)
	}
}

func readVersions(path string) ([]Version, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var versions []Version
	for _, doc := range strings.Split(string(data), "\n---\n") {
		doc = strings.TrimSpace(doc)
		if doc == "" {
			continue
		}
		var v Version
		if err := yaml.Unmarshal([]byte(doc), &v); err != nil {
			return nil, err
		}
		if v.ID != "" {
			versions = append(versions, v)
		}
	}
	return versions, nil
}

func generateGraph(versions []Version) config.OperatorConfig {
	datastores := make(map[string]struct{})
	for _, v := range versions {
		for ds := range v.Config {
			datastores[ds] = struct{}{}
		}
	}

	channels := make([]updates.Channel, 0, len(datastores))
	for ds := range datastores {
		nodes := nodesForDatastore(versions, ds)
		channels = append(channels, updates.Channel{
			Name: "stable",
			Metadata: map[string]string{
				"datastore": ds,
				"default":   "true",
			},
			Nodes: toStates(nodes),
			Edges: generateEdges(nodes),
		})
	}
	sort.Slice(channels, func(i, j int) bool {
		return channels[i].Metadata["datastore"] < channels[j].Metadata["datastore"]
	})

	return config.OperatorConfig{
		ImageName:   "ghcr.io/authzed/spicedb",
		UpdateGraph: updates.UpdateGraph{Channels: channels},
	}
}

type versionNode struct {
	id         string
	tag        string
	migration  string
	phase      string
	deprecated bool
	waypoint   bool
	sv         semver.Version
}

func parseSemver(id string) (semver.Version, error) {
	return semver.ParseTolerant(strings.TrimPrefix(id, "v"))
}

func mustParseSemver(id string) semver.Version {
	v, err := parseSemver(id)
	if err != nil {
		panic(fmt.Sprintf("invalid semver %q: %v", id, err))
	}
	return v
}

func nodesForDatastore(versions []Version, ds string) []versionNode {
	nodes := make([]versionNode, 0, len(versions))
	for _, v := range versions {
		cfg, ok := v.Config[ds]
		if !ok {
			continue
		}
		nodes = append(nodes, versionNode{
			id:         v.ID,
			tag:        v.Tag,
			migration:  cfg.Migration,
			phase:      cfg.Phase,
			deprecated: cfg.Deprecated,
			waypoint:   cfg.Waypoint,
			sv:         mustParseSemver(v.ID),
		})
	}
	// Sort newest first for the channel node list.
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[j].sv.LT(nodes[i].sv)
	})
	return nodes
}

func toStates(nodes []versionNode) []updates.State {
	states := make([]updates.State, len(nodes))
	for i, n := range nodes {
		states[i] = updates.State{
			ID:         n.id,
			Tag:        n.tag,
			Migration:  n.migration,
			Phase:      n.phase,
			Deprecated: n.deprecated,
		}
	}
	return states
}

// generateEdges creates upgrade edges between versions.
//
// Rules:
//   - Every version gets edges to all newer non-deprecated versions.
//   - Phase nodes (nodes with a Phase set) and explicit waypoints act as mandatory
//     stops: if a waypoint W exists between `from` and `to`, the edge is omitted.
func generateEdges(nodes []versionNode) map[string][]string {
	waypoints := make([]semver.Version, 0)
	for _, n := range nodes {
		if n.phase != "" || n.waypoint {
			waypoints = append(waypoints, n.sv)
		}
	}
	sort.Slice(waypoints, func(i, j int) bool {
		return waypoints[i].LT(waypoints[j])
	})

	edges := make(map[string][]string)
	for _, from := range nodes {
		for _, to := range nodes {
			if to.deprecated {
				continue
			}
			if !from.sv.LT(to.sv) {
				continue
			}
			skip := false
			for _, w := range waypoints {
				if from.sv.LT(w) && w.LT(to.sv) {
					skip = true
					break
				}
			}
			if skip {
				continue
			}
			edges[from.id] = append(edges[from.id], to.id)
		}
		sort.Slice(edges[from.id], func(i, j int) bool {
			return mustParseSemver(edges[from.id][i]).LT(mustParseSemver(edges[from.id][j]))
		})
	}
	return edges
}
