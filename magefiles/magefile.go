//go:build mage
// +build mage

package main

import (
	"fmt"
	"os/exec"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
	kind "sigs.k8s.io/kind/pkg/cluster"
	"sigs.k8s.io/kind/pkg/cmd"
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
	goCmd := "go"
	if hasBinary("richgo") {
		goCmd = "richgo"
	}
	return sh.RunV(goCmd, "test", "./...")
}

// Runs the end-to-end tests in a kind cluster
func (Test) E2e() error {
	mg.Deps(checkDocker)
	fmt.Println("running e2e tests")
	return sh.RunWithV(map[string]string{
		"PROVISION": "true",
	}, "go", "run", "github.com/onsi/ginkgo/v2/ginkgo", "--tags=e2e", "-p", "-r", "-v", "--fail-fast", "--randomize-all", "--race", "e2e")
}

// Removes the kind cluster used for end-to-end tests
func (Test) Clean_e2e() error {
	mg.Deps(checkDocker)
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
