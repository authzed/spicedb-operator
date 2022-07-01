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

Next, you'll install the operator:

```console
kubectl apply -k github.com/authzed/spicedb-operator/config
```

Finally you can create your first cluster:

```console
kubectl apply -f - <<EOF
---
apiVersion: v1
kind: Namespace
metadata:
  name: spicedb
---
apiVersion: authzed.com/v1alpha1
kind: SpiceDBCluster
metadata:
  name: dev-spicedb
  namespace: spicedb
spec:
  config:
    replicas: 2
    datastoreEngine: postgres
  secretName: dev-spicedb-config
---
apiVersion: v1
kind: Secret
metadata:
  name: dev-spicedb-config
  namespace: spicedb
stringData:
  datastore_uri: "postgresql:///the-url-of-your-datastore"
  preshared_key: "averysecretpresharedkey" 
EOF
```
