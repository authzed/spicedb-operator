//go:build mage
// +build mage

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"

	"github.com/cespare/xxhash/v2"
	"github.com/jzelinskie/stringz"
	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
	"github.com/magefile/mage/target"
	kind "sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cmd"
	"sigs.k8s.io/kind/pkg/fs"
)

var Aliases = map[string]interface{}{
	"test":     Test.Unit,
	"e2e":      Test.E2e,
	"generate": Gen.All,
}

type Test mg.Namespace

// Runs the unit tests
func (Test) Unit() error {
	fmt.Println("running unit tests")
	return sh.RunV(goCmdForTests(), "test", "./...")
}

const (
	DefaultProposedGraphFile  = "proposed-update-graph.yaml"
	DefaultValidatedGraphFile = "validated-update-graph.yaml"
)

// Runs the end-to-end tests in a kind cluster
func (Test) E2e() error {
	mg.Deps(checkDocker, Gen{}.generateGraphIfSourcesChanged)
	fmt.Println("running e2e tests")

	proposedGraphFile := stringz.DefaultEmpty(os.Getenv("PROPOSED_GRAPH_FILE"), DefaultProposedGraphFile)
	validatedGraphFile := stringz.DefaultEmpty(os.Getenv("VALIDATED_GRAPH_FILE"), DefaultValidatedGraphFile)

	if err := sh.RunWithV(map[string]string{
		"PROVISION":            "true",
		"SPICEDB_CMD":          os.Getenv("SPICEDB_CMD"),
		"SPICEDB_ENV_PREFIX":   os.Getenv("SPICEDB_ENV_PREFIX"),
		"ARCHIVES":             os.Getenv("ARCHIVES"),
		"IMAGES":               os.Getenv("IMAGES"),
		"PROPOSED_GRAPH_FILE":  proposedGraphFile,
		"VALIDATED_GRAPH_FILE": validatedGraphFile,
	}, "go", "run", "github.com/onsi/ginkgo/v2/ginkgo", "--tags=e2e", "-p", "-r", "-vv", "--fail-fast", "--randomize-all", "--flake-attempts=3", "e2e"); err != nil {
		return err
	}

	equal, err := fileEqual(proposedGraphFile, validatedGraphFile)
	if err != nil {
		return err
	}

	if !equal {
		fmt.Println("marking update graph as validated after successful test run")
		return fs.CopyFile(proposedGraphFile, validatedGraphFile)
	}
	fmt.Println("no changes to update graph")

	return nil
}

// Removes the kind cluster used for end-to-end tests
func (Test) Clean_e2e() error {
	mg.Deps(checkDocker)
	fmt.Println("removing saved cluster state")
	if err := os.RemoveAll("./e2e/cluster-state"); err != nil {
		return err
	}
	fmt.Println("removing kind cluster")
	return kind.NewProvider(
		kind.ProviderWithLogger(cmd.NewLogger()),
	).Delete("spicedb-operator-e2e", "")
}

type Gen mg.Namespace

// Run all generators in parallel
func (g Gen) All() error {
	mg.Deps(g.Api, g.Graph)
	return nil
}

// Run kube api codegen
func (Gen) Api() error {
	fmt.Println("generating apis")
	return sh.RunV("go", "generate", "./...")
}

// Generate the update graph
func (Gen) Graph() error {
	fmt.Println("generating update graph")
	return sh.RunV("go", "generate", "./tools/generate-update-graph/main.go")
}

// If the update graph definition
func (g Gen) generateGraphIfSourcesChanged() error {
	regen, err := target.Dir("proposed-update-graph.yaml", "tools/generate-update-graph")
	if err != nil {
		return err
	}
	if regen {
		return g.Graph()
	}
	return nil
}

func checkDocker() error {
	if !hasBinary("docker") {
		return fmt.Errorf("docker must be installed to run e2e tests")
	}
	err := sh.Run("docker", "ps")
	if err == nil || sh.ExitStatus(err) == 0 {
		return nil
	}
	return err
}

func hasBinary(binaryName string) bool {
	_, err := exec.LookPath(binaryName)
	return err == nil
}

func goCmdForTests() string {
	if hasBinary("richgo") {
		return "richgo"
	}
	return "go"
}

func fileEqual(a, b string) (bool, error) {
	aFile, err := os.Open(a)
	if err != nil {
		return false, err
	}
	aHash := xxhash.New()
	_, err = io.Copy(aHash, aFile)
	if err != nil {
		return false, err
	}
	bFile, err := os.Open(b)
	if err != nil {
		return false, err
	}
	bHash := xxhash.New()
	_, err = io.Copy(bHash, bFile)
	if err != nil {
		return false, err
	}

	return aHash.Sum64() == bHash.Sum64(), nil
}
