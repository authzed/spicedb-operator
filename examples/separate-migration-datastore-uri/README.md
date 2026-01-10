# Separate Migration Datastore URI

This example demonstrates how to use separate database connection strings for migrations versus normal SpiceDB operation.

## Overview

The SpiceDB operator supports using different database credentials for:

- **Migrations**: Often require elevated privileges (CREATE TABLE, DROP TABLE, ALTER TABLE)
- **Application Runtime**: Should use least-privilege credentials (SELECT, INSERT, UPDATE, DELETE)

This separation follows security best practices by ensuring the SpiceDB application pods don't have unnecessary database permissions.

## How it Works

When the operator detects a `migration_datastore_uri` key in the secret, it will:

1. Use `migration_datastore_uri` for migration jobs
2. Continue using `datastore_uri` for the SpiceDB application pods

## Example Configuration

See [spicedb-cluster.yaml](spicedb-cluster.yaml) for the complete example.

The key part is the secret configuration:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: spicedb-config
stringData:
  # Used by SpiceDB application pods - limited privileges
  datastore_uri: "postgresql://spicedb_user:password@postgres:5432/spicedb?sslmode=require"
  
  # Used by migration jobs - elevated privileges
  migration_datastore_uri: "postgresql://spicedb_admin:admin_password@postgres:5432/spicedb?sslmode=require"
  
  preshared_key: "your-secure-preshared-key"
```

## Database User Setup

Here's an example of how to set up the PostgreSQL users:

```sql
-- Create the admin user (for migrations)
CREATE USER spicedb_admin WITH PASSWORD 'admin_password';
GRANT CREATE ON DATABASE spicedb TO spicedb_admin;
GRANT ALL PRIVILEGES ON SCHEMA public TO spicedb_admin;

-- Create the application user (for runtime)
CREATE USER spicedb_user WITH PASSWORD 'password';

-- After migrations are run, grant appropriate permissions
-- The migration will create the tables, then you can run:
GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO spicedb_user;
GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO spicedb_user;
```

## Security Considerations

- Store credentials in a proper secret management system
- Use strong, unique passwords for both users
- Consider using SSL/TLS for database connections
- Regularly rotate credentials
- Monitor database access logs
