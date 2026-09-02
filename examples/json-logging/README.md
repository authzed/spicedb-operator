# JSON Logging for SpiceDB Operator

This example demonstrates how to configure the SpiceDB Operator to output logs in JSON format, which is useful for integration with log aggregation systems like ELK (Elasticsearch, Logstash, Kibana) stack, Splunk, or other structured logging solutions.

## Overview

By default, the SpiceDB Operator uses a text-based log format. However, for production environments where logs need to be parsed and analyzed by log aggregation systems, JSON format is often preferred.

## Configuration

### Using the Command Line Flag

The simplest way to enable JSON logging is by using the `--log-format` flag when starting the operator:

```bash
spicedb-operator run --log-format=json
```

### In Kubernetes Deployment

To enable JSON logging in a Kubernetes deployment, modify the operator's deployment manifest:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: spicedb-operator
  namespace: spicedb-operator
spec:
  template:
    spec:
      containers:
      - name: spicedb-operator
        image: authzed/spicedb-operator:latest
        args:
        - "run"
        - "--log-format=json"
        # ... other flags
```

## Log Format Comparison

### Text Format (Default)

```text
2024-01-15T10:30:45.123Z	INFO	controller	Reconciling SpiceDBCluster	{"namespace": "default", "name": "my-spicedb"}
2024-01-15T10:30:45.456Z	INFO	controller	Created deployment	{"namespace": "default", "name": "my-spicedb-spicedb"}
```

### JSON Format

```json
{"level":"info","ts":"2024-01-15T10:30:45.123Z","logger":"controller","msg":"Reconciling SpiceDBCluster","namespace":"default","name":"my-spicedb"}
{"level":"info","ts":"2024-01-15T10:30:45.456Z","logger":"controller","msg":"Created deployment","namespace":"default","name":"my-spicedb-spicedb"}
```

## Integration with ELK Stack

When using JSON logging with the ELK stack:

1. **Filebeat Configuration**: Configure Filebeat to read the operator logs:

   ```yaml
   filebeat.inputs:
   - type: container
     paths:
       - /var/log/containers/spicedb-operator-*.log
     processors:
       - decode_json_fields:
           fields: ["message"]
           target: ""
           overwrite_keys: true
   ```

2. **Logstash Pipeline**: No special parsing is needed as the logs are already structured.

3. **Kibana**: You can directly query and visualize the structured log fields.

## Benefits

- **Structured Data**: Each log entry is a valid JSON object with consistent fields
- **Easy Parsing**: No need for complex regular expressions to parse log entries
- **Better Search**: Log aggregation systems can index individual fields for faster searching
- **Standardized Format**: Compatible with most modern logging infrastructure
- **Machine-Readable**: Easier to process logs programmatically for alerting or analysis

## Complete Example

See the [operator-with-json-logging.yaml](operator-with-json-logging.yaml) file for a complete example of deploying the SpiceDB Operator with JSON logging enabled.
