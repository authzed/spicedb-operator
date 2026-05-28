# How to contribute

## Communication

- Bug Reports & Feature Requests: [GitHub Issues]
- Questions: [GitHub Discussions] or [Discord]

All communication in these forums abides by our [Code of Conduct].

[GitHub Issues]: https://github.com/authzed/spicedb-operator/issues
[Code of Conduct]: CODE-OF-CONDUCT.md
[Github Discussions]: https://github.com/orgs/authzed/discussions/new?category=q-a
[Discord]: https://authzed.com/discord

## Creating issues

If any part of the project has a bug or documentation mistakes, please let us know by opening an issue.
All bugs and mistakes are considered very seriously, regardless of complexity.

Before creating an issue, please check that an issue reporting the same problem does not already exist.
To make the issue accurate and easy to understand, please try to create issues that are:

- Unique -- do not duplicate existing bug report.
  Duplicate bug reports will be closed.
- Specific -- include as much details as possible: which version, what environment, what configuration, etc.
- Reproducible -- include the steps to reproduce the problem.
  Some issues might be hard to reproduce, so please do your best to include the steps that might lead to the problem.
- Isolated -- try to isolate and reproduce the bug with minimum dependencies.
  It would significantly slow down the speed to fix a bug if too many dependencies are involved in a bug report.
  Debugging external systems that rely on this project is out of scope, but guidance or help using the project itself is fine.
- Scoped -- one bug per report.
  Do not follow up with another bug inside one report.

It may be worthwhile to read [Elika Etemad’s article on filing good bug reports][filing-good-bugs] before creating a bug report.

Maintainers might ask for further information to resolve an issue.

[filing-good-bugs]: http://fantasai.inkedblade.net/style/talks/filing-good-bugs/

## Finding issues

You can find issues by priority: [Urgent], [High], [Medium], [Low], [Maybe].
There are also [good first issues].

[Urgent]: https://github.com/authzed/spicedb/labels/priority%2F0%20urgent
[High]: https://github.com/authzed/spicedb/labels/priority%2F1%20high
[Medium]: https://github.com/authzed/spicedb/labels/priority%2F2%20medium
[Low]: https://github.com/authzed/spicedb/labels/priority%2F3%20low
[Maybe]: https://github.com/authzed/spicedb/labels/priority%2F4%20maybe
[good first issues]: https://github.com/authzed/spicedb/labels/hint%2Fgood%20first%20issue

## Contribution flow

This is a rough outline of what a contributor's workflow looks like:

- Create an issue
- Fork the project
- Create a [feature branch]
- Push changes to your branch
- Submit a pull request
- Respond to feedback from project maintainers
- Rebase to squash related and fixup commits
- Get LGTM from reviewer(s)
- Merge with a merge commit

Creating new issues is one of the best ways to contribute.
You have no obligation to offer a solution or code to fix an issue that you open.
If you do decide to try and contribute something, please submit an issue first so that a discussion can occur to avoid any wasted efforts.

[feature branch]: https://www.atlassian.com/git/tutorials/comparing-workflows/feature-branch-workflow

## Legal requirements

In order to protect the project, all contributors are required to sign our [Contributor License Agreement][cla] before their contribution is accepted.

The signing process has been automated by [CLA Assistant][cla-assistant] during the Pull Request review process and only requires responding with a comment acknowledging the agreement.

[cla]: https://github.com/authzed/cla/blob/main/v1/icla.md
[cla-assistant]: https://github.com/cla-assistant/cla-assistant

## Common tasks

### Testing & building a binary

In order to build and test the project, the [latest stable version of Go] and knowledge of a [working Go environment] are required.

[latest stable version of Go]: https://golang.org/dl
[working Go environment]: https://golang.org/doc/code.html

Install [mage](https://magefile.org/#installation):

```sh
# homebrew, see link for other options
brew install mage
```

Run e2e tests:

```sh
mage test:e2e
```

Run unit tests:

```sh
mage test:unit
```

### Trying local changes

Once you have a build, you'll usually want to run the operator against a real Kubernetes cluster to validate your changes end-to-end.
The instructions below assume you're working from the root of a clone of this repository.

#### In a [kind](https://kind.sigs.k8s.io) cluster

This is the recommended path for local development because it doesn't require pushing images to an external registry.

1. Build the operator image locally:

   ```sh
   docker build --tag spicedb-operator:dev .
   ```

2. Create a kind cluster if you don't already have one, and load the image into it:

   ```sh
   kind create cluster --name spicedb-operator-dev
   kind load docker-image spicedb-operator:dev --name spicedb-operator-dev
   ```

3. Deploy the operator manifests, overriding the image tag to your locally built one:

   ```sh
   kubectl apply --server-side -k config
   kubectl -n spicedb-operator set image deployment/spicedb-operator \
     spicedb-operator=spicedb-operator:dev
   ```

4. After rebuilding and reloading the image, restart the operator pod to pick up the new image:

   ```sh
   docker build --tag spicedb-operator:dev .
   kind load docker-image spicedb-operator:dev --name spicedb-operator-dev
   kubectl -n spicedb-operator delete pod -l app=spicedb-operator
   ```

Tail the operator logs while iterating:

```sh
kubectl -n spicedb-operator logs -f deployment/spicedb-operator
```

#### In a remote / full-fledged cluster

If you need to test against a cluster that can't pull from your local Docker daemon, push the image to a registry the cluster can reach.

```sh
REGISTRY=ghcr.io/<your-org>
docker build --tag ${REGISTRY}/spicedb-operator:dev .
docker push ${REGISTRY}/spicedb-operator:dev
kubectl -n spicedb-operator set image deployment/spicedb-operator \
  spicedb-operator=${REGISTRY}/spicedb-operator:dev
kubectl -n spicedb-operator rollout restart deployment/spicedb-operator
```

If the operator isn't already installed on the cluster, install it first as described in the [README](README.md#getting-started), then run the commands above to swap in your local image.

### Adding dependencies

This project does not use anything other than the standard [Go modules] toolchain for managing dependencies.

[Go modules]: https://golang.org/ref/mod

```sh
go get github.com/org/newdependency@version
```

Continuous integration enforces that `go mod tidy` has been run.

### Regenerating `proposed-update-graph.yaml`

The update graph can be regenerated whenever there is a new spicedb release.
CI will validate all new edges when there are changes to `proposed-update-graph.yaml` and will copy them into `config/update-graph.yaml` if successful.

```go
mage gen:graph
```
