# SpiceDB Operator

[![Container Image](https://img.shields.io/github/v/release/authzed/spicedb-operator?color=%232496ED&label=container&logo=docker "Container Image")](https://hub.docker.com/r/authzed/spicedb-operator/tags)
[![Docs](https://img.shields.io/badge/docs-authzed.com-%234B4B6C "Authzed Documentation")](https://docs.authzed.com)
[![Build Status](https://github.com/authzed/spicedb-operator/workflows/Build%20&%20Test/badge.svg "GitHub Actions")](https://github.com/authzed/spicedb-operator/actions)
[![Discord Server](https://img.shields.io/discord/844600078504951838?color=7289da&logo=discord "Discord Server")](https://discord.gg/jTysUaxXzM)
[![Twitter](https://img.shields.io/twitter/follow/authzed?color=%23179CF0&logo=twitter&style=flat-square "@authzed on Twitter")](https://twitter.com/authzed)

A Kubernetes controller for managing instances of [SpiceDB]

Features:

- Create, manage, and scale fully-configured SpiceDB clusters with a single [Custom Resource]
- Run migrations automatically when upgrading SpiceDB versions

See [CONTRIBUTING.md] for instructions on how to contribute and perform common tasks like building the project and running tests.

## Quickstart

The [quickstart.yaml] has definitions that will:

- Run cockroachdb
- Create a `spicedb` namespace
- Install the operator and the CRDs
- Create a 3 node spicedb cluster configured to talk to the cockroach cluster

We recommend using a local cluster like [kind] or [docker destktop].

## Example

```yaml
apiVersion: authzed.com/v1alpha1
kind: SpiceDBCluster
metadata:
  name: spicedb
  namespace: spice
spec:
  # config for spicedb
  config:
    replicas: "2"
    datastoreEngine: cockroachdb
  # a secret in the same namespace 
  secretName: spicedb
status:
  image: authzed-spicedb-enterprise:dev
  observedGeneration: 1
  secretHash: hashOfSecret
---
apiVersion: v1
kind: Secret
metadata:
  name: spicedb
  namespace: spice
data:
  datastore_uri: "postgresql:///the-url-of-your-datastore"
  preshared_key: "averysecretpresharedkey" 
```

[SpiceDB]: https://github.com/authzed/spicedb
[CONTRIBUTING.md]: CONTRIBUTING.md
[Custom Resource]: https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/
[quickstart.yaml]: quickstart.yaml
