#!/usr/bin/env python3
"""
Semantic diff of two update-graph YAML files.

Treats the `channels` list as an unordered set keyed by (name, datastore metadata).
Reports per-channel differences in nodes and edges.

Usage:
    python3 diff-update-graphs.py <old-graph.yaml> <new-graph.yaml>
    python3 diff-update-graphs.py  # defaults to old-proposed-update-graph.yaml vs proposed-update-graph.yaml
"""

import sys
import yaml


def load_graph(path):
    with open(path) as f:
        return yaml.safe_load(f)


def index_channels(graph):
    idx = {}
    for ch in graph.get("channels", []):
        key = (ch["name"], ch.get("metadata", {}).get("datastore", ""))
        idx[key] = ch
    return idx


def semver_sort_key(s):
    # Simple sort key: split on dots and dashes, numeric parts as ints
    import re
    return [int(p) if p.isdigit() else p for p in re.split(r"[.\-]", s.lstrip("v"))]


def diff_graphs(old_path, new_path):
    old = load_graph(old_path)
    new = load_graph(new_path)

    old_ch = index_channels(old)
    new_ch = index_channels(new)

    old_keys = set(old_ch.keys())
    new_keys = set(new_ch.keys())

    print(f"Comparing:\n  OLD: {old_path}\n  NEW: {new_path}\n")

    only_old = sorted(old_keys - new_keys)
    only_new = sorted(new_keys - old_keys)

    if only_old:
        print("=== Channels only in OLD ===")
        for k in only_old:
            print(f"  {k}")
        print()

    if only_new:
        print("=== Channels only in NEW ===")
        for k in only_new:
            print(f"  {k}")
        print()

    print("=== Per-channel comparison ===")
    any_diff = False
    for key in sorted(old_keys & new_keys):
        oc = old_ch[key]
        nc = new_ch[key]

        old_nodes = {n["id"]: n for n in oc.get("nodes", [])}
        new_nodes = {n["id"]: n for n in nc.get("nodes", [])}

        only_old_nodes = sorted(set(old_nodes) - set(new_nodes), key=semver_sort_key)
        only_new_nodes = sorted(set(new_nodes) - set(old_nodes), key=semver_sort_key)

        old_edges = {k: set(v) for k, v in (oc.get("edges") or {}).items()}
        new_edges = {k: set(v) for k, v in (nc.get("edges") or {}).items()}

        all_from_nodes = set(old_edges) | set(new_edges)
        edge_diffs = {}
        for fn in all_from_nodes:
            oe = old_edges.get(fn, set())
            ne = new_edges.get(fn, set())
            only_old_e = sorted(oe - ne, key=semver_sort_key)
            only_new_e = sorted(ne - oe, key=semver_sort_key)
            if only_old_e or only_new_e:
                edge_diffs[fn] = (only_old_e, only_new_e)

        # Node field-level diffs
        field_diffs = {}
        for nid in sorted(set(old_nodes) & set(new_nodes), key=semver_sort_key):
            on = old_nodes[nid]
            nn = new_nodes[nid]
            diffs = {}
            for f in set(on) | set(nn):
                if on.get(f) != nn.get(f):
                    diffs[f] = (on.get(f), nn.get(f))
            if diffs:
                field_diffs[nid] = diffs

        channel_label = f"{key[1]}/{key[0]}"
        if not only_old_nodes and not only_new_nodes and not edge_diffs and not field_diffs:
            print(f"\n  {channel_label}: IDENTICAL")
        else:
            any_diff = True
            print(f"\n  {channel_label}: DIFFERS")
            if only_old_nodes:
                print(f"    Nodes only in OLD: {only_old_nodes}")
            if only_new_nodes:
                print(f"    Nodes only in NEW: {only_new_nodes}")
            for fn in sorted(edge_diffs, key=semver_sort_key):
                only_old_e, only_new_e = edge_diffs[fn]
                if only_old_e:
                    print(f"    Edge {fn} -> REMOVED targets: {only_old_e}")
                if only_new_e:
                    print(f"    Edge {fn} -> ADDED targets:   {only_new_e}")
            for nid, diffs in sorted(field_diffs.items(), key=lambda x: semver_sort_key(x[0])):
                print(f"    Node {nid} field diffs: {diffs}")

    if not any_diff and not only_old and not only_new:
        print("\nGraphs are SEMANTICALLY EQUIVALENT (order-agnostic).")


if __name__ == "__main__":
    if len(sys.argv) == 3:
        old_path, new_path = sys.argv[1], sys.argv[2]
    elif len(sys.argv) == 1:
        old_path = "old-proposed-update-graph.yaml"
        new_path = "proposed-update-graph.yaml"
    else:
        print("Usage: diff-update-graphs.py [<old.yaml> <new.yaml>]")
        sys.exit(1)

    diff_graphs(old_path, new_path)
