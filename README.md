# Authzed Operator

## Architecture

- The operator watches all namespaces in a cluster.
- The `Stack` object holds config for a single Authzed Enterprise stack
- Other "dependent" objects (deployments, services, etc) will be created in response to a stack.
- The operator watches all `Stack` objects in the cluster, unless they are marked explicitly `authzed.com/unmanaged` (for debugging)
- The operator only watches dependent objects that are labelled for a stack.
- A change in a dependent resource only triggers re-reconciliation of the `Stack`, which is then checked for consistency.
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

Run against apiserver+etcd only (not all tests will run):
```sh
PROVISION=false APISERVER_ONLY=true ginkgo --tags=e2e -r
```

Run with `go test` (ginkgo has better signal handling, prefer ginkgo to `go test`)
```sh
go test -tags=e2e ./...
```

## Notes

- `client-go` is used instead of frameworks like kubebuilder/operator-sdk/kudo
- Design closely mimics modern kubernetes controllers, see references
- Re-uses patterns from k/k when possible

## References

- https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md
- https://github.com/kubernetes/community/blob/master/contributors/devel/sig-api-machinery/controllers.md
- https://github.com/kcp-dev/kcp/
- https://github.com/kubernetes/kubernetes/tree/master/staging/src/k8s.io/controller-manager
- https://github.com/openshift/cluster-monitoring-operator/blob/07a11b1094072e1d1eea32939ac22f3e4abff095/pkg/alert/rule_controller.go
- https://github.com/operator-framework/operator-lifecycle-manager/blob/master/pkg/controller/operators/olm/operator.go
- https://book.kubebuilder.io/reference/markers/rbac.html