#!/bin/sh
set -eu

output=$1
artifact=$2
package_name=$3
package_version=$4
pg_major=$5
architecture=$6
ecosystem=$7
source_revision=$8
shift 8

json_escape() {
  printf '%s' "$1" | sed 's/\\/\\\\/g; s/"/\\"/g'
}

artifact_name=$(basename "$artifact")
artifact_sha=$(sha256sum "$artifact" | awk '{print $1}')
timestamp=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
serial="urn:uuid:$(cat /proc/sys/kernel/random/uuid)"

{
  printf '{\n'
  printf '  "$schema": "https://cyclonedx.org/schema/bom-1.5.schema.json",\n'
  printf '  "bomFormat": "CycloneDX",\n'
  printf '  "specVersion": "1.5",\n'
  printf '  "serialNumber": "%s",\n' "$serial"
  printf '  "version": 1,\n'
  printf '  "metadata": {"timestamp":"%s","tools":{"components":[{"type":"application","name":"agentsql-binder-container-packager","version":"0.4"}]},"component":{"type":"library","bom-ref":"pkg:agentsql/%s@%s?pg=%s&arch=%s&ecosystem=%s","name":"%s","version":"%s","hashes":[{"alg":"SHA-256","content":"%s"}],"properties":[{"name":"agentsql:artifact","value":"%s"},{"name":"agentsql:source-revision","value":"%s"},{"name":"agentsql:postgres-major","value":"%s"},{"name":"agentsql:architecture","value":"%s"},{"name":"agentsql:ecosystem","value":"%s"}]}},\n' \
    "$timestamp" "$(json_escape "$package_name")" "$(json_escape "$package_version")" "$pg_major" "$architecture" "$ecosystem" \
    "$(json_escape "$package_name")" "$(json_escape "$package_version")" "$artifact_sha" "$(json_escape "$artifact_name")" \
    "$(json_escape "$source_revision")" "$pg_major" "$architecture" "$ecosystem"
  printf '  "components": [\n'
  first=1
  refs=
  for component in "$@"; do
    name=$(printf '%s' "$component" | cut -d '|' -f 1)
    version=$(printf '%s' "$component" | cut -d '|' -f 2)
    role=$(printf '%s' "$component" | cut -d '|' -f 3)
    ref="pkg:generic/$(json_escape "$name")@$(json_escape "$version")?ecosystem=$ecosystem"
    if [ "$first" -eq 0 ]; then printf ',\n'; fi
    first=0
    printf '    {"type":"library","bom-ref":"%s","name":"%s","version":"%s","scope":"required","properties":[{"name":"agentsql:role","value":"%s"}]}' \
      "$ref" "$(json_escape "$name")" "$(json_escape "$version")" "$(json_escape "$role")"
    refs="${refs}${refs:+,}\"$ref\""
  done
  printf '\n  ],\n'
  printf '  "dependencies": [{"ref":"pkg:agentsql/%s@%s?pg=%s&arch=%s&ecosystem=%s","dependsOn":[%s]}]\n' \
    "$(json_escape "$package_name")" "$(json_escape "$package_version")" "$pg_major" "$architecture" "$ecosystem" "$refs"
  printf '}\n'
} > "$output"

