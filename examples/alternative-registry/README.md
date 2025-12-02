# Using Alternative Container Registry

This example demonstrates how to configure the SpiceDB operator to use an alternative container registry instead of the default one.

## Overview

The SpiceDB operator supports specifying a custom base image for SpiceDB containers through the `baseImage` field in the `SpiceDBCluster` spec. This is useful when:

- You need to use a private container registry
- You want to mirror images to your own registry for security or compliance reasons
- You need to use a registry proxy for better performance
- You're running in an air-gapped environment

## Configuration

The image selection follows this precedence order (highest to lowest):

1. `.spec.config.image` with explicit tag/digest (overrides everything)
2. `.spec.baseImage` field (what this example uses)
3. The operator's `--base-image` flag
4. The `imageName` defined in the update graph

**Important:** The `baseImage` field must NOT contain a tag (`:tag`) or digest (`@sha256:...`). The operator will automatically append the appropriate tag based on the `version` or `channel` you specify. If you need to specify an exact image with tag, use `.spec.config.image` instead.

## Example

See [spicedb-cluster.yaml](spicedb-cluster.yaml) for a complete example.

```yaml
apiVersion: authzed.com/v1alpha1
kind: SpiceDBCluster
metadata:
  name: example-with-custom-registry
spec:
  # Specify your alternative registry here (NO TAG!)
  baseImage: "my-registry.company.com/authzed/spicedb"
  
  # The operator will append the appropriate tag based on the version/channel
  version: "v1.33.0"
  
  config:
    datastoreEngine: postgres
    # ... other config
  
  # If using a private registry, use patches to add imagePullSecrets
  patches:
    - kind: Deployment
      patch: |
        spec:
          template:
            spec:
              imagePullSecrets:
                - name: registry-credentials
```

## How it Works

When you specify a `baseImage`, the operator will:

1. Use your specified registry as the base
2. Append the appropriate tag or digest based on the `version` or `channel` you specify
3. The final image will be: `<baseImage>:<tag>` or `<baseImage>@<digest>`

For example, if you specify:

- `baseImage: "my-registry.company.com/authzed/spicedb"`
- `version: "v1.33.0"`

The operator will use: `my-registry.company.com/authzed/spicedb:v1.33.0`

## Private Registry Authentication

If your alternative registry requires authentication, you need to:

1. Create an image pull secret with your registry credentials:

   ```bash
   kubectl create secret docker-registry registry-credentials \
     --docker-server=my-registry.company.com \
     --docker-username=YOUR-USERNAME \
     --docker-password=YOUR-PASSWORD \
     --namespace=spicedb-custom-registry
   ```

2. Use the `patches` field to inject the image pull secret into the deployment:

   ```yaml
   spec:
     patches:
       - kind: Deployment
         patch: |
           spec:
             template:
               spec:
                 imagePullSecrets:
                   - name: registry-credentials
   ```

## Common Mistakes

### Including a tag in baseImage

**Wrong:**

```yaml
spec:
  baseImage: "my-registry.company.com/authzed/spicedb:v1.33.0"  # Don't include tag!
```

**Correct:**

```yaml
spec:
  baseImage: "my-registry.company.com/authzed/spicedb"
  version: "v1.33.0"
```

### Confusing baseImage with config.image

- Use `baseImage` when you want the operator to manage versions via the update graph
- Use `config.image` (with full tag/digest) when you want to bypass the update graph entirely

## Important Notes

- Make sure your Kubernetes nodes can pull from your alternative registry
- If using a private registry, use the `patches` field to configure image pull secrets
- The operator still uses the update graph to determine valid versions and migration paths
- The alternative registry must contain the exact same images as the official registry
