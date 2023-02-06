package main

import (
	"fmt"
	"log"
	"os"

	"github.com/authzed/controller-idioms/hash"
	"golang.org/x/exp/maps"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/yaml"

	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/updates"
)

// Usage: main.go <proposed graph> <existing graph>

// Every node and edge in the existing graph is assumed to be correct
// Every node and edge in the proposed graph that is not in the existing
// graph will be validated.

//go:generate go run main.go ../../default-operator-config.yaml

const (
	githubNamespace  = "authzed"
	githubRepository = "spicedb"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Println("must provide path to update graph file")
		os.Exit(1)
	}

	// load proposed graph
	proposedGraphFile, err := os.Open(os.Args[1])
	if err != nil {
		log.Fatalf("error loading graph file: %v", err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(proposedGraphFile, 100)
	var proposedGraph config.OperatorConfig
	if err := decoder.Decode(&proposedGraph); err != nil {
		log.Fatalf("error decoding graph file: %v", err)
	}

	existingGraph := config.NewOperatorConfig()

	// if we have been provided a validation record, load it
	if len(os.Args) > 2 {
		existingGraphFile, err := os.Open(os.Args[1])
		if err != nil {
			log.Fatalf("error loading graph file: %v", err)
		}
		decoder = yaml.NewYAMLOrJSONDecoder(existingGraphFile, 100)
		if err := decoder.Decode(&existingGraph); err != nil {
			log.Fatalf("error decoding graph file: %v", err)
		}
	}

	errs := make([]error, 0)
	// validate every node that's not already validated
	for _, proposed := range proposedGraph.Channels {
		for _, existing := range existingGraph.Channels {
			if proposed.EqualIdentity(existing) {
				// validate new nodes

				for _, node := range proposed.Nodes {

				}

				// validate new edges
			}
		}
	}

	// validate every edge that's not already validated
}

func newNodes(proposed, existing []updates.State) []updates.State {
	proposedSet := hash.NewSet(proposed...)
	existingSet := hash.NewSet(existing...)
	return maps.Values(proposedSet.SetDifference(existingSet))
}

func newEdges(proposed, existing updates.EdgeSet) updates.EdgeSet {

}

func validateNode(state updates.State) error {
	if len(state.ID) == 0 {
		return fmt.Errorf("node missing identifier: %v", state)
	}
	if len(state.Tag) == 0 && len(state.Digest) == 0 {
		return fmt.Errorf("node must specify image or tag: %v", state)
	}

	// validate tag if present
	if len(state.Tag) > 0 {

	}

	// validate digest if present
	if len(state.Digest) > 0 {

	}

	// validate tag matches digest if both are specified

	// validate that migration is valid

	// validate that migration is head if phase == ""

	// validate that phase is valid for that binary

	// validate that

	return nil
}
