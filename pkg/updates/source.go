package updates

// Source models a single stream of updates for an installed version.
type Source interface {
	// NextDirect returns the newest version that has an edge and has
	// no required migrations
	NextDirect(from string) string

	// Next returns the newest version that has an edge (but may include
	// migrations)
	Next(from string) string

	// Latest returns the newest version that has some path through the graph
	Latest(from string) string

	// State returns information about the node,
	// i.e. what image and migration to run
	State(id string) State

	// Source returns a new source that is a subgraph of the current source,
	// but where `head` is set to `to`
	Source(to string) (Source, error)
}
