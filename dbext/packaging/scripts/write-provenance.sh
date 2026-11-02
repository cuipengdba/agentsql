#!/bin/sh
set -eu

output=$1
artifact=$2
pg_major=$3
architecture=$4
ecosystem=$5
source_revision=$6
builder_image=$7

if ! printf '%s' "$source_revision" | grep -Eq '^[0-9a-f]{40}$'; then
  echo 'a full lowercase Git commit is required for package provenance' >&2
  exit 1
fi

artifact_name=$(basename "$artifact")
artifact_sha=$(sha256sum "$artifact" | awk '{print $1}')
source_sha=$(cd /src && sha256sum Makefile agentsql_binder.c agentsql_binder.control agentsql_binder--0.4.sql | sha256sum | awk '{print $1}')
timestamp=$(date -u '+%Y-%m-%dT%H:%M:%SZ')

cat > "$output" <<EOF
{
  "_type": "https://in-toto.io/Statement/v1",
  "subject": [{"name":"$artifact_name","digest":{"sha256":"$artifact_sha"}}],
  "predicateType": "https://slsa.dev/provenance/v1",
  "predicate": {
    "buildDefinition": {
      "buildType": "https://agentsql.dev/build-types/postgresql-extension-package/v1",
      "externalParameters": {"postgresMajor":"$pg_major","architecture":"$architecture","ecosystem":"$ecosystem","sourceRevision":"$source_revision"},
      "resolvedDependencies": [
        {"uri":"git+https://github.com/cuipengdba/agentsql@$source_revision","digest":{"gitCommit":"$source_revision"}},
        {"uri":"urn:agentsql:dbext/postgres/agentsql_binder:source-file-list","digest":{"sha256":"$source_sha"}}
      ]
    },
    "runDetails": {
      "builder": {"id":"$builder_image"},
      "metadata": {"invocationId":"local-$artifact_sha","startedOn":"$timestamp","finishedOn":"$timestamp"},
      "byproducts": [{"name":"DEV ONLY - local unsigned build prior to detached signing"}]
    }
  }
}
EOF

