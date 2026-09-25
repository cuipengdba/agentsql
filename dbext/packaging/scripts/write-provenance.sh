#!/bin/sh
set -eu

output=$1
artifact=$2
pg_major=$3
architecture=$4
ecosystem=$5
source_revision=$6
builder_image=$7

artifact_name=$(basename "$artifact")
artifact_sha=$(sha256sum "$artifact" | awk '{print $1}')
source_sha=$(sha256sum /src/agentsql_binder.c /src/agentsql_binder.control /src/agentsql_binder--0.4.sql /src/Makefile | sha256sum | awk '{print $1}')
timestamp=$(date -u '+%Y-%m-%dT%H:%M:%SZ')

cat > "$output" <<EOF
{
  "_type": "https://in-toto.io/Statement/v1",
  "subject": [{"name":"$artifact_name","digest":{"sha256":"$artifact_sha"}}],
  "predicateType": "https://slsa.dev/provenance/v1",
  "predicate": {
    "buildDefinition": {
      "buildType": "https://agentsql.dev/build-types/postgresql-extension-package/v1",
      "externalParameters": {"postgresMajor":"$pg_major","architecture":"$architecture","ecosystem":"$ecosystem"},
      "resolvedDependencies": [{"uri":"git+https://invalid.local/agentsql@$source_revision","digest":{"sha256":"$source_sha"}}]
    },
    "runDetails": {
      "builder": {"id":"$builder_image"},
      "metadata": {"invocationId":"local-$artifact_sha","startedOn":"$timestamp","finishedOn":"$timestamp"},
      "byproducts": [{"name":"DEV ONLY - local unsigned build prior to detached signing"}]
    }
  }
}
EOF

