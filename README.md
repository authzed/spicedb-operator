# SpiceDB Operator

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
PROVISION=true IMAGES=authzed-spicedb-enterprise:dev ginkgo --tags=e2e -r
```

Run with `go test` (ginkgo has better signal handling, prefer ginkgo to `go test`)
```sh
go test -tags=e2e ./...
```