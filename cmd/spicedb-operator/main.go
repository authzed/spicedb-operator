package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/authzed/spicedb-operator/pkg/cmd/run"
	"github.com/authzed/spicedb-operator/pkg/version"
)

func main() {
	root := &cobra.Command{
		Use:     os.Args[0],
		Short:   "an operator for managing SpiceDB clusters",
		Version: version.Version,
	}

	root.AddCommand(run.NewCmdRun(run.RecommendedOptions()))

	var includeDeps bool
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "display operator version information",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println(version.UsageVersion(includeDeps))
		},
	}
	versionCmd.Flags().BoolVar(&includeDeps, "include-deps", false, "include dependencies' versions")
	root.AddCommand(versionCmd)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}
