//go:build mage
// +build mage

package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const golangciLintVersion = "v2.12.2"

type Lint mg.Namespace

// All Run all linters
func (l Lint) All() error {
	mg.SerialDeps(l.Go, l.Extra)
	return nil
}

// Go Run all go linters
func (l Lint) Go() error {
	if err := (Gen{}).All(); err != nil {
		return err
	}
	mg.SerialDeps(l.Golangcilint, l.Tidy)
	return nil
}

// Golangcilint Run golangci-lint
func (Lint) Golangcilint() error {
	fmt.Println("running golangci-lint")
	return sh.RunV("go", "run",
		"github.com/golangci/golangci-lint/v2/cmd/golangci-lint@"+golangciLintVersion,
		"run", "--fix",
		"-c", ".golangci.yaml")
}

// Tidy Run go mod tidy on every module in the repo.
// ./magefiles is intentionally omitted: it has no separate go.mod and
// resolves to the root module, so tidying the root already covers it.
// Add it here if a magefiles/go.mod is ever introduced.
func (Lint) Tidy() error {
	fmt.Println("tidying go modules")
	dirs := []string{".", "./tools", "./e2e"}
	for _, dir := range dirs {
		if err := runDirV(dir, "go", "mod", "tidy"); err != nil {
			return fmt.Errorf("go mod tidy in %s: %w", dir, err)
		}
	}
	return nil
}

// Extra Lint everything that's not code
func (l Lint) Extra() error {
	mg.Deps(l.Yaml, l.Markdown, l.Kustomize)
	return nil
}

// Yaml Lint yaml
func (Lint) Yaml() error {
	mg.Deps(checkDocker)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return sh.RunV("docker", "run", "--rm",
		"-w", "/src",
		"-v", fmt.Sprintf("%s:/src:ro", cwd),
		"cytopia/yamllint:1", "-c", "/src/.yamllint", ".")
}

// Markdown Lint markdown
func (Lint) Markdown() error {
	mg.Deps(checkDocker)
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return sh.RunV("docker", "run", "--rm",
		"-w", "/src",
		"-v", fmt.Sprintf("%s:/src:ro", cwd),
		"ghcr.io/igorshubovych/markdownlint-cli:v0.47.0",
		"--config", "/src/.markdownlint.yaml", ".")
}

// Kustomize Verify kustomize build succeeds
func (Lint) Kustomize() error {
	fmt.Println("running kustomize build ./config")
	if !hasBinary("kustomize") {
		return errors.New("kustomize must be installed to run this lint target")
	}
	return sh.RunV("kustomize", "build", "./config")
}
