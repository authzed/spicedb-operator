package updates

import (
	"fmt"

	"golang.org/x/exp/maps"
)

// EdgeSet maps a node id to a list of node ids that it can update to
type EdgeSet map[string][]string

// NodeSet maps a node id to an index in the OrderedNodes array
type NodeSet map[string]int

// MemorySource is an in-memory implementation of Source.
// It's an oracle to answer update questions for an installed version.
type MemorySource struct {
	// OrderedNodes is an ordered list of all nodes. Lower index == newer version.
	OrderedNodes []State
	// Nodes is a helper to lookup a node by id
	Nodes NodeSet
	// Edges contains the edgeset for this source.
	Edges EdgeSet
}

// Next returns the newest version that can be installed in one step.
func (m *MemorySource) Next(from string) string {
	if edges, ok := m.Edges[from]; ok && len(edges) > 0 {
		return edges[len(edges)-1]
	}
	return ""
}

// NextDirect returns the newest version that can be directly installed without
// running any migrations.
func (m *MemorySource) NextDirect(from string) (found string) {
	initial := m.OrderedNodes[m.Nodes[from]]
	if to, ok := m.Edges[from]; ok && len(to) > 0 {
		for _, n := range m.Edges[from] {
			node := m.OrderedNodes[m.Nodes[n]]

			// if the phase and migration match the current node, no migrations
			// are required
			if initial.Phase == node.Phase && initial.Migration == node.Migration {
				found = n
			} else {
				break
			}
		}
	}
	return found
}

// Latest returns the newest version that can be installed. If different
// from `Next`, that means multiple steps are required (i.e. a multi-phase
// migration, or a required stopping point in a series of updates).
func (m *MemorySource) Latest(id string) string {
	if len(m.OrderedNodes) == 0 || id == m.OrderedNodes[0].ID {
		return ""
	}
	return m.OrderedNodes[0].ID
}

func (m *MemorySource) State(id string) State {
	index, ok := m.Nodes[id]
	if !ok {
		return State{}
	}
	return m.OrderedNodes[index]
}

func (m *MemorySource) Source(to string) (Source, error) {
	// copy the ordered node list from `to` onward
	var index int
	if len(to) > 0 {
		index = m.Nodes[to]
	}
	orderedNodes := make([]State, len(m.OrderedNodes)-index)
	copy(orderedNodes, m.OrderedNodes[index:len(m.OrderedNodes)])

	nodeSet := make(map[string]int, len(orderedNodes))
	for i, n := range orderedNodes {
		nodeSet[n.ID] = i
	}

	edges := make(map[string][]string)
	for from, to := range m.Edges {
		// skip edges where from is not in the node set
		if _, ok := nodeSet[from]; !ok {
			continue
		}
		_, ok := edges[from]
		if !ok {
			edges[from] = make([]string, 0)
		}
		for _, n := range to {
			// skip edges where to is not in the node set
			if _, ok := nodeSet[n]; !ok {
				continue
			}
			edges[from] = append(edges[from], n)
		}
	}

	return newMemorySourceFromValidatedNodes(nodeSet, edges, orderedNodes)
}

func (m *MemorySource) validateAllNodesPathToHead() error {
	head := m.OrderedNodes[0].ID
	for _, n := range m.OrderedNodes {
		if n.ID == head {
			continue
		}
		visited := make(map[string]struct{}, 0)
		// chasing current should lead to head
		for current := m.Next(n.ID); current != head; current = m.Next(current) {
			if _, ok := visited[current]; ok {
				return fmt.Errorf("channel cycle detected: %v", append(maps.Keys(visited), current))
			}
			if current == "" {
				return fmt.Errorf("there is no path from %s to %s", n.ID, m.OrderedNodes[0].ID)
			}
			visited[current] = struct{}{}
		}
	}
	return nil
}

func NewMemorySource(nodes []State, edges EdgeSet) (Source, error) {
	if len(nodes) == 0 || len(edges) == 0 {
		return nil, fmt.Errorf("no edges or no nodes")
	}

	nodeSet := make(map[string]int, len(nodes))
	for i, n := range nodes {
		if _, ok := nodeSet[n.ID]; ok {
			return nil, fmt.Errorf("more than one node with ID %s", n.ID)
		}
		nodeSet[n.ID] = i
	}

	for from, toSet := range edges {
		// ensure all edges reference nodes
		if _, ok := nodeSet[from]; !ok {
			return nil, fmt.Errorf("node list is missing node %s", from)
		}
		for _, to := range toSet {
			if _, ok := nodeSet[to]; !ok {
				return nil, fmt.Errorf("node list is missing node %s", to)
			}
		}
		if len(toSet) != 0 {
			continue
		}

		// The only node with no updates should be the head of the channel
		// i.e. the first thing in the ordered node list
		if from != nodes[0].ID {
			return nil, fmt.Errorf("%s has no outgoing edges, but it is not the head of the channel", from)
		}
	}

	return newMemorySourceFromValidatedNodes(nodeSet, edges, nodes)
}

func newMemorySourceFromValidatedNodes(nodeSet map[string]int, edges map[string][]string, nodes []State) (Source, error) {
	source := &MemorySource{Nodes: nodeSet, Edges: edges, OrderedNodes: nodes}

	if err := source.validateAllNodesPathToHead(); err != nil {
		return nil, err
	}

	// TODO: validate that the adjacency lists are in the same order as the node list
	// so that we can always assume further down the list is newer
	return source, nil
}
