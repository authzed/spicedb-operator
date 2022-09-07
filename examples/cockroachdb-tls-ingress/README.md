# SpiceDB with CockroachDB, TLS, and Ingress

This will guide you through setting up a complete example SpiceDB cluster:

- Managed by the operator
- With multiple nodes that dispatch to each other (with TLS)
- With a cockroachdb backing datastore
- With ingress (and TLS)

## Configure Root CA

We recommend using [mkcert] to generate a [Certificate Authority] for local development.

[mkcert]: https://github.com/FiloSottile/mkcert
[Certificate Authority]: https://en.wikipedia.org/wiki/Certificate_authority

The following will generate a local CA using mkcert:

```sh
mkcert -install
```

Regardless of how you generate your key pair, afterwards we'll need to place it in the component used for ingress:

```sh
export MKCERTROOT=`mkcert -CAROOT`
cp $MKCERTROOT/rootCA-key.pem ingress/tls.key
cp $MKCERTROOT/rootCA.pem ingress/tls.crt
```

## Create a Cluster

Create a local (or remote) kubernetes cluster with the tool of your choice:

We recommend one of the following:

- [Docker Desktop](https://www.docker.com/products/docker-desktop/)
- [kind](https://kind.sigs.k8s.io)
- [minikube](https://minikube.sigs.k8s.io)

For ingress to work locally, you will need to map port `443` locally:

### Docker Desktop

The default config maps port `80` and `443`, so no changes are needed.

### kind

Configure kind to map the ports when creating the cluster:

```sh
cat <<EOF | kind create cluster --config=-
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  kubeadmConfigPatches:
  - |
    kind: InitConfiguration
    nodeRegistration:
      kubeletExtraArgs:
        node-labels: "ingress-ready=true"
  extraPortMappings:
  - containerPort: 80
    hostPort: 80
    protocol: TCP
  - containerPort: 443
    hostPort: 443
    protocol: TCP
EOF
```

### Minikube

See the minikube docs on [accessing apps](https://minikube.sigs.k8s.io/docs/handbook/accessing/).

## Apply the example manifests

Ensure kubectl is pointing to your cluster with `kubectl config current-context`, and then apply the example manifests with:

```sh
kubectl apply --server-side -k  .
```

It is safe and may be necessary to run this multiple times if any of the resources fail to apply.
CRDs especially may fail to create, so check the output for them.

## Connect with `zed`

If you haven't already, make sure you've installed [zed](https://github.com/authzed/zed#installation).

Now you can use zed to interact with SpiceDB:

```sh
zed context set local localhost:443 averysecretpresharedkey

zed schema write <(cat << EOF
definition user {}

definition post {
	relation reader: user
	relation writer: user

	permission read = reader + writer
	permission write = writer
}
EOF
)

zed schema read
```

## Clean up

Clean up the changes from this example with:

```sh
kubectl delete -k .
```
