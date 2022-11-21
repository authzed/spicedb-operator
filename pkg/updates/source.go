package updates

// Source models a single stream of updates for an installed version.
type Source interface {
	// NextVersionWithoutMigrations returns the newest version that has an edge that
	// does not require any migrations.
	NextVersionWithoutMigrations(from string) string

	// Next returns the newest version that has an edge.
	// This version might include migrations.
	NextVersion(from string) string

	// Latest returns the newest version that has some path through the
	// graph.
	//
	// If no path exists, returns the empty string.
	//
	// If different from `NextVersion`, that means multiple steps are
	// required (i.e. a multi-phase migration, or a required stopping point
	// in a series of updates).
	LatestVersion(from string) string

	// State returns the information that is required to update to the provided
	// node.
	State(id string) State

	// Subgraph returns a new Source that is a subgraph of the current source,
	// but where `head` is set to the provided node.
	Subgraph(head string) (Source, error)
}
