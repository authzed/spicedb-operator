# Update Graph Diff: old-proposed-update-graph.yaml vs proposed-update-graph.yaml

The graphs are **not fully equivalent**. Below is every difference found.

---

## `memory/stable`: IDENTICAL — no differences.

---

## All four DB channels: `v1.37.1` edge set differs

All of cockroachdb, mysql, postgres, and spanner are affected.

| Graph | `v1.37.1` edges to |
|-------|-------------------|
| Old | `v1.38.0, v1.39.1, v1.40.1, v1.42.1, v1.45.4, v1.47.1, v1.48.0, v1.49.2, v1.51.1` |
| New | `v1.38.0` only |

The old code let `v1.37.1` skip past `v1.38.0`; the new waypoint algorithm blocks that
because `v1.38.0` is now a mandatory stop (`waypoint: true`).

---

## Phase nodes have broader outgoing edges in the new graph

The old code treated phase nodes as strict "step through me to the immediate next version"
nodes. The new waypoint algorithm allows phase nodes to reach any target up to (but not
past) the next waypoint. Affected nodes:

- **`cockroachdb/stable`**: `v1.30.0-phase1`
  - Old: only `→ v1.30.0`
  - New: `→ v1.30.0` through `v1.36.2`
- **`postgres/stable`**: `v1.14.0-phase2`
  - Old: only `→ v1.14.0`
  - New: `→ v1.14.0` through `v1.36.2`
- **`spanner/stable`**: `v1.22.2-phase2` and `v1.29.5-phase1`
  - Old: each pointed only to the immediate next version
  - New: each can reach further (up to the `v1.38.0` waypoint)

---

## `spanner/stable`: `v1.51.1` node missing from old graph

The old graph encoded the latest spanner version as a quirk: node `v1.49.2` had `tag:
v1.51.1`. The new graph correctly has `v1.49.2` with `tag: v1.49.2` and a separate
`v1.51.1` node, which adds outgoing edges from all existing spanner nodes to `v1.51.1`.

---

## Node field differences

| Channel | Node | Field | Old value | New value |
|---------|------|-------|-----------|-----------|
| `cockroachdb/stable` | `v1.30.0-phase1` | `phase` | missing | `write-both-read-new` |
| `spanner/stable` | `v1.29.5-phase1` | `phase` | missing | `write-both-read-new` |
| `spanner/stable` | `v1.49.2` | `tag` | `v1.51.1` | `v1.49.2` |

---

## Bottom line

The graphs differ in four ways:

1. **`v1.37.1` edge narrowing** (all DB channels) — likely correct; `v1.38.0` is now a
   hard waypoint.
2. **Phase-node outgoing edge expansion** — semantic change: old code was "phase node →
   immediate next release only"; new algorithm is "phase node → anything up to the next
   waypoint". Whether multi-hop skips from a phase node are safe depends on whether
   those intermediate versions require a migration stop.
3. **`spanner/stable` `v1.51.1` node added** — likely a bug fix in the old graph where
   `v1.49.2` was carrying the wrong tag.
4. **`phase` field missing on phase nodes in old graph** — old serialization omitted the
   `phase` field from those nodes; new graph includes it.
