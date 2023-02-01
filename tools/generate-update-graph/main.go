package main

import (
	"context"
	"fmt"
	"os"

	"github.com/google/go-github/v43/github"
	"sigs.k8s.io/yaml"

	"github.com/authzed/spicedb-operator/pkg/config"
	"github.com/authzed/spicedb-operator/pkg/updates"
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

	opconfig := config.OperatorConfig{
		ImageName: "ghcr.io/authzed/spicedb",
		UpdateGraph: updates.UpdateGraph{Channels: []updates.Channel{
			postgresChannel(),
			crdbChannel(),
			mysqlChannel(),
			spannerChannel(),
		}},
	}

	for _, c := range opconfig.Channels {
		if c.Nodes[0].Tag != *latestRelease.Name {
			fmt.Printf("channel %q does not contain the latest release %q\n", c.Name, *latestRelease.Name)
			os.Exit(1)
			return
		}
	}

	yamlBytes, err := yaml.Marshal(&opconfig)
	if err != nil {
		panic(err)
	}

	if err := os.WriteFile(os.Args[1], yamlBytes, 0o666); err != nil {
		panic(err)
	}
}

func postgresChannel() updates.Channel {
	releases := []updates.State{
		{ID: "v1.16.1", Tag: "v1.16.1", Migration: "drop-bigserial-ids"},
		{ID: "v1.16.0", Tag: "v1.16.0", Migration: "drop-bigserial-ids"},
		{ID: "v1.15.0", Tag: "v1.15.0", Migration: "drop-bigserial-ids"},
		{ID: "v1.14.1", Tag: "v1.14.1", Migration: "drop-bigserial-ids"},
		{ID: "v1.14.0", Tag: "v1.14.0", Migration: "drop-bigserial-ids"},
		{ID: "v1.14.0-phase2", Tag: "v1.14.0", Migration: "add-xid-constraints", Phase: "write-both-read-new"},
		{ID: "v1.14.0-phase1", Tag: "v1.14.0", Migration: "add-xid-columns", Phase: "write-both-read-old"},
		{ID: "v1.13.0", Tag: "v1.13.0", Migration: "add-ns-config-id"},
		{ID: "v1.12.0", Tag: "v1.12.0", Migration: "add-ns-config-id"},
		{ID: "v1.11.0", Tag: "v1.11.0", Migration: "add-ns-config-id"},
		{ID: "v1.10.0", Tag: "v1.10.0", Migration: "add-ns-config-id"},
		{ID: "v1.9.0", Tag: "v1.9.0", Migration: "add-unique-datastore-id"},
		{ID: "v1.8.0", Tag: "v1.8.0", Migration: "add-unique-datastore-id"},
		{ID: "v1.7.1", Tag: "v1.7.1", Migration: "add-unique-datastore-id"},
		{ID: "v1.7.0", Tag: "v1.7.0", Migration: "add-unique-datastore-id"},
		{ID: "v1.6.0", Tag: "v1.6.0", Migration: "add-unique-datastore-id"},
		{ID: "v1.5.0", Tag: "v1.5.0", Migration: "add-transaction-timestamp-index"},
		{ID: "v1.4.0", Tag: "v1.4.0", Migration: "add-transaction-timestamp-index"},
		{ID: "v1.3.0", Tag: "v1.3.0", Migration: "add-transaction-timestamp-index"},
		{ID: "v1.2.0", Tag: "v1.2.0", Migration: "add-transaction-timestamp-index"},
		{ID: "v1.1.0", Tag: "v1.1.0", Migration: "add-transaction-timestamp-index"},
		{ID: "v1.0.0", Tag: "v1.0.0", Migration: "add-unique-living-ns"},
	}
	return updates.Channel{
		Name: "stable",
		Metadata: map[string]string{
			"datastore": "postgres",
			"default":   "true",
		},
		Nodes: releases,
		Edges: map[string][]string{
			"v1.16.0":        {"v1.16.1"},
			"v1.15.0":        {"v1.16.0", "v1.16.1"},
			"v1.14.1":        {"v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.14.0":        {"v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.14.0-phase2": {"v1.14.0"},
			"v1.14.0-phase1": {"v1.14.0-phase2"},
			"v1.13.0":        {"v1.14.0-phase1"},
			"v1.12.0":        {"v1.13.0", "v1.14.0-phase1"},
			"v1.11.0":        {"v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.10.0":        {"v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.9.0":         {"v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.8.0":         {"v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.7.1":         {"v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.7.0":         {"v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.6.0":         {"v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.5.0":         {"v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.4.0":         {"v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.3.0":         {"v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.2.0":         {"v1.3.0", "v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.1.0":         {"v1.2.0", "v1.3.0", "v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
			"v1.0.0":         {"v1.1.0", "v1.2.0", "v1.3.0", "v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.0-phase1"},
		},
	}
}

func crdbChannel() updates.Channel {
	releases := []updates.State{
		{ID: "v1.16.1", Tag: "v1.16.1", Migration: "add-caveats"},
		{ID: "v1.16.0", Tag: "v1.16.0", Migration: "add-caveats"},
		{ID: "v1.15.0", Tag: "v1.15.0", Migration: "add-caveats"},
		{ID: "v1.14.1", Tag: "v1.14.1", Migration: "add-caveats"},
		{ID: "v1.14.0", Tag: "v1.14.0", Migration: "add-caveats"},
		{ID: "v1.13.0", Tag: "v1.13.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.12.0", Tag: "v1.12.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.11.0", Tag: "v1.11.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.10.0", Tag: "v1.10.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.9.0", Tag: "v1.9.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.8.0", Tag: "v1.8.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.7.1", Tag: "v1.7.1", Migration: "add-metadata-and-counters"},
		{ID: "v1.7.0", Tag: "v1.7.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.6.0", Tag: "v1.6.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.5.0", Tag: "v1.5.0", Migration: "add-transactions-table"},
		{ID: "v1.4.0", Tag: "v1.4.0", Migration: "add-transactions-table"},
		{ID: "v1.3.0", Tag: "v1.3.0", Migration: "add-transactions-table"},
		{ID: "v1.2.0", Tag: "v1.2.0", Migration: "add-transactions-table"},
		{ID: "v1.1.0", Tag: "v1.1.0", Migration: "add-transactions-table"},
		{ID: "v1.0.0", Tag: "v1.0.0", Migration: "add-transactions-table"},
	}
	return updates.Channel{
		Name: "stable",
		Metadata: map[string]string{
			"datastore": "cockroachdb",
			"default":   "true",
		},
		Nodes: releases,
		Edges: map[string][]string{
			"v1.16.0": {"v1.16.1"},
			"v1.15.0": {"v1.16.0", "v1.16.1"},
			"v1.14.1": {"v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.14.0": {"v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.13.0": {"v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.12.0": {"v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.11.0": {"v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.10.0": {"v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.9.0":  {"v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.8.0":  {"v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.7.1":  {"v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.7.0":  {"v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.6.0":  {"v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.5.0":  {"v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.4.0":  {"v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.3.0":  {"v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.2.0":  {"v1.3.0", "v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.1.0":  {"v1.2.0", "v1.3.0", "v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.0.0":  {"v1.1.0", "v1.2.0", "v1.3.0", "v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
		},
	}
}

func mysqlChannel() updates.Channel {
	releases := []updates.State{
		{ID: "v1.16.1", Tag: "v1.16.1", Migration: "add-caveats"},
		{ID: "v1.16.0", Tag: "v1.16.0", Migration: "add-caveats"},
		{ID: "v1.15.0", Tag: "v1.15.0", Migration: "add-caveats"},
		{ID: "v1.14.1", Tag: "v1.14.1", Migration: "add-caveats"},
		{ID: "v1.14.0", Tag: "v1.14.0", Migration: "add-caveats"},
		{ID: "v1.13.0", Tag: "v1.13.0", Migration: "add_ns_config_id"},
		{ID: "v1.12.0", Tag: "v1.12.0", Migration: "add_ns_config_id"},
		{ID: "v1.11.0", Tag: "v1.11.0", Migration: "add_ns_config_id"},
		{ID: "v1.10.0", Tag: "v1.10.0", Migration: "add_ns_config_id"},
		{ID: "v1.9.0", Tag: "v1.9.0", Migration: "add_unique_datastore_id"},
		{ID: "v1.8.0", Tag: "v1.8.0", Migration: "add_unique_datastore_id"},
		{ID: "v1.7.1", Tag: "v1.7.1", Migration: "add_unique_datastore_id"},
		{ID: "v1.7.0", Tag: "v1.7.0", Migration: "add_unique_datastore_id"},
	}
	return updates.Channel{
		Name: "stable",
		Metadata: map[string]string{
			"datastore": "mysql",
			"default":   "true",
		},
		Nodes: releases,
		Edges: map[string][]string{
			"v1.16.0": {"v1.16.1"},
			"v1.15.0": {"v1.16.0", "v1.16.1"},
			"v1.14.1": {"v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.14.0": {"v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.13.0": {"v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.12.0": {"v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.11.0": {"v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.10.0": {"v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.9.0":  {"v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.8.0":  {"v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.7.1":  {"v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.7.0":  {"v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
		},
	}
}

func spannerChannel() updates.Channel {
	releases := []updates.State{
		{ID: "v1.16.1", Tag: "v1.16.1", Migration: "add-caveats"},
		{ID: "v1.16.0", Tag: "v1.16.0", Migration: "add-caveats"},
		{ID: "v1.15.0", Tag: "v1.15.0", Migration: "add-caveats"},
		{ID: "v1.14.1", Tag: "v1.14.1", Migration: "add-caveats"},
		{ID: "v1.14.0", Tag: "v1.14.0", Migration: "add-caveats"},
		{ID: "v1.13.0", Tag: "v1.13.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.12.0", Tag: "v1.12.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.11.0", Tag: "v1.11.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.10.0", Tag: "v1.10.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.9.0", Tag: "v1.9.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.8.0", Tag: "v1.8.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.7.1", Tag: "v1.7.1", Migration: "add-metadata-and-counters"},
		{ID: "v1.7.0", Tag: "v1.7.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.6.0", Tag: "v1.6.0", Migration: "add-metadata-and-counters"},
		{ID: "v1.5.0", Tag: "v1.5.0", Migration: "initial"},
		{ID: "v1.4.0", Tag: "v1.4.0", Migration: "initial"},
		{ID: "v1.3.0", Tag: "v1.3.0", Migration: "initial"},
		{ID: "v1.2.0", Tag: "v1.2.0", Migration: "initial"},
		{ID: "v1.1.0", Tag: "v1.1.0", Migration: "initial"},
		{ID: "v1.0.0", Tag: "v1.0.0", Migration: "initial"},
	}
	return updates.Channel{
		Name: "stable",
		Metadata: map[string]string{
			"datastore": "spanner",
			"default":   "true",
		},
		Nodes: releases,
		Edges: map[string][]string{
			"v1.16.0": {"v1.16.1"},
			"v1.15.0": {"v1.16.0", "v1.16.1"},
			"v1.14.1": {"v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.14.0": {"v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.13.0": {"v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.12.0": {"v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.11.0": {"v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.10.0": {"v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.9.0":  {"v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.8.0":  {"v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.7.1":  {"v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.7.0":  {"v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.6.0":  {"v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.5.0":  {"v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.4.0":  {"v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.3.0":  {"v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.2.0":  {"v1.3.0", "v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.1.0":  {"v1.2.0", "v1.3.0", "v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
			"v1.0.0":  {"v1.1.0", "v1.2.0", "v1.3.0", "v1.4.0", "v1.5.0", "v1.6.0", "v1.7.1", "v1.8.0", "v1.9.0", "v1.10.0", "v1.11.0", "v1.12.0", "v1.13.0", "v1.14.1", "v1.15.0", "v1.16.0", "v1.16.1"},
		},
	}
}
