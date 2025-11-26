# AWS IAM Roles for Service Accounts (IRSA) with SpiceDB

This example demonstrates how to configure SpiceDB to use AWS IAM Roles for Service Accounts (IRSA) on Amazon EKS clusters.

## Overview

IRSA allows Kubernetes pods to assume AWS IAM roles without storing long-lived AWS credentials. This is the recommended approach for granting AWS permissions to SpiceDB when running on EKS, as it:

- Eliminates the need to store AWS credentials in secrets
- Provides automatic credential rotation
- Enables fine-grained access control per service account
- Follows AWS security best practices

The `extraServiceAccountAnnotations` field in the SpiceDBCluster spec allows you to add annotations to the ServiceAccount created by the operator, which is required for IRSA to work.

## Prerequisites

1. **EKS Cluster with OIDC Provider**: Your EKS cluster must have an OIDC provider associated with it.

   ```bash
   eksctl utils associate-iam-oidc-provider --cluster <cluster-name> --approve
   ```

2. **Create an IAM Role**: Create an IAM role with the necessary permissions (e.g., `rds-db:connect` for RDS IAM authentication).

3. **Configure Trust Relationship**: Update the role's trust policy to allow the SpiceDB service account to assume it:

   ```json
   {
     "Version": "2012-10-17",
     "Statement": [
       {
         "Effect": "Allow",
         "Principal": {
           "Federated": "arn:aws:iam::123456789012:oidc-provider/oidc.eks.region.amazonaws.com/id/EXAMPLED539D4633E53DE1B716D3041E"
         },
         "Action": "sts:AssumeRoleWithWebIdentity",
         "Condition": {
           "StringEquals": {
             "oidc.eks.region.amazonaws.com/id/EXAMPLED539D4633E53DE1B716D3041E:sub": "system:serviceaccount:default:spicedb-with-irsa",
             "oidc.eks.region.amazonaws.com/id/EXAMPLED539D4633E53DE1B716D3041E:aud": "sts.amazonaws.com"
           }
         }
       }
     ]
   }
   ```

   > **Note**: The service account name in the trust policy (`spicedb-with-irsa`) must match the SpiceDBCluster name. If you use a custom `serviceAccountName` in your config, use that name instead.

## Configuration

### Basic IRSA Setup

The key configuration is adding the `eks.amazonaws.com/role-arn` annotation to the service account:

```yaml
apiVersion: authzed.com/v1alpha1
kind: SpiceDBCluster
metadata:
  name: spicedb-with-irsa
spec:
  config:
    datastoreEngine: postgres
    extraServiceAccountAnnotations:
      eks.amazonaws.com/role-arn: "arn:aws:iam::123456789012:role/your-iam-role"
  secretName: spicedb-config
```

### Custom Service Account Name

You can specify a custom service account name using the `serviceAccountName` field:

```yaml
spec:
  config:
    serviceAccountName: "my-custom-sa"
    extraServiceAccountAnnotations:
      eks.amazonaws.com/role-arn: "arn:aws:iam::123456789012:role/your-iam-role"
```

> **Important**: If you use a custom service account name, update your IAM trust policy to reference `system:serviceaccount:<namespace>:<serviceAccountName>`.

## Use Case: RDS with IAM Authentication

Instead of storing database passwords, you can use IAM authentication with RDS. This requires:

1. The `eks.amazonaws.com/role-arn` annotation for IRSA
2. The `datastoreCredentialsProviderName` config set to `aws-iam`

```yaml
apiVersion: authzed.com/v1alpha1
kind: SpiceDBCluster
metadata:
  name: spicedb-with-irsa
  namespace: default
spec:
  config:
    datastoreEngine: postgres
    # Enable AWS IAM credential provider for RDS authentication
    datastoreCredentialsProviderName: "aws-iam"
    extraServiceAccountAnnotations:
      eks.amazonaws.com/role-arn: "arn:aws:iam::123456789012:role/spicedb-rds-role"
  secretName: spicedb-config
---
apiVersion: v1
kind: Secret
metadata:
  name: spicedb-config
  namespace: default
stringData:
  preshared_key: "your-very-secret-preshared-key"
  # Note: No password in the URI - IAM authentication handles this
  datastore_uri: "postgresql://spicedb_user@your-rds-endpoint.region.rds.amazonaws.com:5432/spicedb?sslmode=require"
```

### IAM Policy for RDS Access

Your IAM role needs the `rds-db:connect` permission:

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": "rds-db:connect",
      "Resource": "arn:aws:rds-db:region:123456789012:dbuser:db-instance-resource-id/spicedb_user"
    }
  ]
}
```

## Deploying the Example

1. Update the IAM role ARN in `spicedb-with-iam-role.yaml` with your actual role ARN
2. Update the RDS endpoint and database user in the secret
3. Apply the configuration:

   ```bash
   kubectl apply -k .
   ```

4. Verify the service account has the correct annotation:

   ```bash
   kubectl get sa spicedb-with-irsa -o jsonpath='{.metadata.annotations}'
   ```

## Troubleshooting

1. **Check Service Account Annotations**:

   ```bash
   kubectl get sa spicedb-with-irsa -o jsonpath='{.metadata.annotations}'
   ```

2. **Verify Pod Has AWS Environment Variables**:

   ```bash
   kubectl exec -it <spicedb-pod> -- env | grep AWS
   ```

   You should see:
   - `AWS_ROLE_ARN`
   - `AWS_WEB_IDENTITY_TOKEN_FILE`

3. **Check IAM Role Trust Policy**: Ensure the trust policy matches your cluster's OIDC provider and service account name (including namespace).

4. **Verify RDS IAM Authentication is Enabled**: Ensure your RDS instance has IAM authentication enabled in the AWS console or via CLI.

5. **Check SpiceDB Logs for Auth Errors**:

   ```bash
   kubectl logs -l app.kubernetes.io/instance=spicedb-with-irsa
   ```

## Additional Resources

- [AWS Documentation on IRSA](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html)
- [SpiceDB Operator Documentation](https://github.com/authzed/spicedb-operator)
- [Using IAM authentication with RDS](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.IAMDBAuth.html)
- [Creating an IAM policy for RDS IAM authentication](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.IAMDBAuth.IAMPolicy.html)
