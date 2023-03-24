# SpiceDB Operator

[![Container Image](https://img.shields.io/github/v/release/authzed/spicedb-operator?color=%232496ED&label=container&logo=docker "Container Image")](https://hub.docker.com/r/authzed/spicedb-operator/tags)
[![Docs](https://img.shields.io/badge/docs-authzed.com-%234B4B6C "Authzed Documentation")](https://docs.authzed.com)
[![Build Status](https://github.com/authzed/spicedb-operator/workflows/Build%20&%20Test/badge.svg "GitHub Actions")](https://github.com/authzed/spicedb-operator/actions)
[![Discord Server](https://img.shields.io/discord/844600078504951838?color=7289da&logo=discord "Discord Server")](https://discord.gg/jTysUaxXzM)
[![Twitter](https://img.shields.io/twitter/follow/authzed?color=%23179CF0&logo=twitter&style=flat-square "@authzed on Twitter")](https://twitter.com/authzed)

A [Kubernetes operator] for managing [SpiceDB] clusters.

Features include:

- Creation, management, and scaling of SpiceDB clusters with a single [Custom Resource]
- Automated datastore migrations when upgrading SpiceDB versions

Have questions? Join our [Discord].

Looking to contribute? See [CONTRIBUTING.md].

[Kubernetes operator]: https://kubernetes.io/docs/concepts/extend-kubernetes/operator/
[SpiceDB]: https://github.com/authzed/spicedb
[Custom Resource]: https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/
[Discord]: https://authzed.com/discord
[CONTRIBUTING.md]: CONTRIBUTING.md

## Getting Started

In order to get started, you'll need a Kubernetes cluster.
For local development, install your tool of choice.
You can use whatever, so long as you're comfortable with it and it works on your platform.
We recommend one of the following:

- [Docker Desktop](https://www.docker.com/products/docker-desktop/)
- [kind](https://kind.sigs.k8s.io)
- [minikube](https://minikube.sigs.k8s.io)

Next, you'll install a [release](https://github.com/authzed/spicedb-operator/releases/) of the operator:

```console
kubectl apply --server-side -f https://github.com/authzed/spicedb-operator/releases/latest/download/bundle.yaml
```

Finally you can create your first cluster:

```console
kubectl apply --server-side -f - <<EOF
apiVersion: authzed.com/v1alpha1
kind: SpiceDBCluster
metadata:
  name: dev
spec:
  config:
    datastoreEngine: memory
  secretName: dev-spicedb-config
---
apiVersion: v1
kind: Secret
metadata:
  name: dev-spicedb-config
stringData:
  preshared_key: "averysecretpresharedkey" 
EOF
```

## Connecting To Your Cluster

If you haven't already, make sure you've installed [zed](https://github.com/authzed/zed#installation).

Port forward the grpc endpoint:

```console
kubectl port-forward deployment/dev-spicedb 50051:50051
```

Now you can use zed to interact with SpiceDB:

```console
zed --insecure --endpoint=localhost:50051 --token=averysecretpresharedkey schema read
```

## Where To Go From Here

- Check out the [examples](examples) directory to see how to configure `SpiceDBCluster` for production, including datastore backends, TLS, and Ingress.
- Learn how to use SpiceDB via the [docs](https://docs.authzed.com/) and [playground](https://play.authzed.com/).
- Ask questions and join the community in [discord](https://authzed.com/discord).

## Automatic and Suggested Updates

The SpiceDB operator now ships with a set of release channels for SpiceDB.
Release channels allow the operator to walk through a safe series of updates, like the [phased migration for postgres in SpiceDB v1.14.0](https://github.com/authzed/spicedb/releases/tag/v1.14.0)

There are two ways you can choose to use update channels:

- automatic updates
- suggested updates

Which mode you choose depends on your tolerance for uncertainty.
If possible, we recommend running a stage or canary instance with automatic updates enabled, and using suggested updates for production and production-like environments.

If no channel is selected, a default (stable) channel will be used for the selected datastore.

Available Update Channels:

| Datastore   | Channels |
|-------------|----------|
| postgres    | stable   |
| cockroachdb | stable   |
| mysql       | stable   |
| spanner     | stable   |
| memory      | stable   |

### Automatic Updates

If you do not specify a `version` that you want to run, the operator will always keep you up to date with the newest version in the channel.

If the operator or the update graph changes, the head of the channel may change and trigger an update.

```yaml
apiVersion: authzed.com/v1alpha1
kind: SpiceDBCluster
metadata:
  name: dev
  namespace: default
spec:
  channel: stable 
  config:
    datastoreEngine: cockroachdb
status:
  currentVersion:
    name: v1.16.1
    channel: stable 
```

### Suggested Updates

Even if you do not want automatic updates, you should choose an update channel - this ensures you do not miss important upgrade steps in phased migrations.

By specifying a `version`, the operator will install the specific version you have requested.
If another version is already running, the operator will walk through the steps defined in the update channel, but will stop once it reaches `version`.
No updates will be taken automatically, you must pick the next version to run and write it into the `spec.version` field.
This keeps SpiceDB updates "on rails" while giving you full control over when and how to roll out updates.

Once you are at the specified `version`, the operator will inform you of available updates in the status of the `SpiceDBCluster`:

```yaml
apiVersion: authzed.com/v1alpha1
kind: SpiceDBCluster
metadata:
  name: dev
spec:
  channel: stable 
  version: v1.14.0
  config:
    datastoreEngine: cockroachdb
status:
  currentVersion:
    name: v1.14.0
    channel: stable 
  availableVersions:
  - name: v1.14.1
    channel: stable
    description: direct update with no migrations
```

Note that it can also show you updates that are available in other channels, if you wish to switch back and forth (be careful! if you switch to another channel and update, there may not be a path to get back to the original channel!)
Only the nearest-neighbor update will be shown for channels other than the current one.

### Force Override

You can opt out of update channels entirely, and force spicedb-operator to install a specific image and manage it as a `spicedb` instance.

This is not recommended, but may be useful for development environments or to try prerelease versions of SpiceDB before they are in an update channel.

```yaml=
apiVersion: authzed.com/v1alpha1
kind: SpiceDBCluster
metadata:
  name: dev
spec:
  config:
    image: ghcr.io/authzed/spicedb:v1.11.0-prerelease
```
