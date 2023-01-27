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
kubectl apply --server-side -f https://github.com/authzed/spicedb-operator/releases/download/v1.1.0/bundle.yaml
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

## Updating SpiceDBClusters

The operator handles the rollout of `SpiceDB` upgrades, inluding coordinating migrations.
By default, the operator will upgrade all `SpiceDBCluster`s that it manages when the operator sees a new default image in the config (see [default-operator-config.yaml](default-operator-config.yaml) for the current default images).
This config can be updated manually, but it is also updated with each release of spicedb-operator and included in the operator image.

If you wish to opt out of automated updates, you can specify an image for the SpiceDBCluster in the config:

```yaml
apiVersion: authzed.com/v1alpha1
kind: SpiceDBCluster
metadata:
  name: dev
spec:
  config:
    image: ghcr.io/authzed/spicedb:v1.11.0
    datastoreEngine: memory 
  secretName: dev-spicedb-config
```

The spicedb-operator will happily attempt to run any image you specify, but if you specify an image that is not in the list of `allowedImages`, `allowedTags`, or `allowedDigests`, the status will warn you:

```yaml
status:
  conditions:
  - lastTransitionTime: "2022-09-02T21:49:19Z"
    message: '["ubuntu" invalid: "ubuntu" is not in the configured list of allowed
      images"]'
    reason: WarningsPresent
    status: "True"
    type: ConfigurationWarning
```
