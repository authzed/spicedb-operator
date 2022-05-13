# SpiceDB Operator

[![Container Image](https://img.shields.io/github/v/release/authzed/spicedb-operator?color=%232496ED&label=container&logo=docker "Container Image")](https://hub.docker.com/r/authzed/spicedb-operator/tags)
[![Docs](https://img.shields.io/badge/docs-authzed.com-%234B4B6C "Authzed Documentation")](https://docs.authzed.com)
[![Build Status](https://github.com/authzed/spicedb-operator/workflows/Build%20&%20Test/badge.svg "GitHub Actions")](https://github.com/authzed/spicedb-operator/actions)
[![Discord Server](https://img.shields.io/discord/844600078504951838?color=7289da&logo=discord "Discord Server")](https://discord.gg/jTysUaxXzM)
[![Twitter](https://img.shields.io/twitter/follow/authzed?color=%23179CF0&logo=twitter&style=flat-square "@authzed on Twitter")](https://twitter.com/authzed)

## Architecture

- The operator watches all namespaces in a cluster.
- The `SpiceDBCluster` object holds config for a single SpiceDB Cluster
- Other "dependent" objects (deployments, services, etc) will be created in response to a `SpiceDBCluster`.
- The operator watches all `SpiceDBCluster` objects in the cluster, unless they are marked explicitly `authzed.com/unmanaged` (for debugging)
- The operator only watches dependent objects that are labelled as a component of a `SpiceDBCluster`.
- A change in a dependent resource only triggers re-reconciliation of the `SpiceDBCluster`, which is then checked for consistency.
  - This may change in the future, but keeps the state machine simple for now.
- Currently there's no leader election; just stop the old operator and start a new one, and only run one.

## Debug

- metrics on `:8080/metrics`
- profiles on `:8080/debug/pprof`
- control log level with `-v=1` to `-v=8`

## Tests

Install ginkgo:

```sh
go install github.com/onsi/ginkgo/v2/ginkgo@v2
```

Run against an existing cluster with images already loaded (current kubeconfig context)

```sh
ginkgo --tags=e2e -r
```

Spin up a new `kind` cluster and run tests:

```sh
PROVISION=true IMAGES=spicedb:dev,spicedb:updated ginkgo --tags=e2e -r
```

Run with `go test` (ginkgo has better signal handling, prefer ginkgo to `go test`)

```sh
go test -tags=e2e ./...
```
