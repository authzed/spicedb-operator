package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/go-github/v43/github"
	"sigs.k8s.io/yaml"

	"github.com/authzed/spicedb-operator/pkg/config"
)

//go:generate go run main.go ../../default-operator-config.yaml

const (
	githubNamespace  = "authzed"
	githubRepository = "spicedb"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Println("must provide filename")
		os.Exit(1)
	}
	ctx := context.Background()
	client := github.NewClient(nil)

	// this returns the newest release by date, not by version
	// note that spicedb uses the same API to determine if it's up to date
	latestRelease, _, err := client.Repositories.GetLatestRelease(ctx, githubNamespace, githubRepository)
	if err != nil {
		panic(err)
	}
	allReleases, _, err := client.Repositories.ListReleases(ctx, githubNamespace, githubRepository, nil)
	if err != nil {
		panic(err)
	}
	allowedTags := make([]string, 0, len(allReleases))
	for _, r := range allReleases {
		allowedTags = append(allowedTags, *r.Name)
	}

	opconfig := config.OperatorConfig{
		ImageName: "ghcr.io/authzed/spicedb",
		ImageTag:  *latestRelease.Name,
		AllowedImages: []string{
			"ghcr.io/authzed/spicedb",
			"authzed/spicedb",
			"quay.io/authzed/spicedb",
		},
		AllowedTags: allowedTags,
	}

	yamlBytes, err := yaml.Marshal(&opconfig)
	if err != nil {
		panic(err)
	}

	if err := os.WriteFile(os.Args[1], yamlBytes, 0o666); err != nil {
		panic(err)
	}
}
