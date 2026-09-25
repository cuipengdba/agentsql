#!/bin/sh
set -eu

usage() {
  cat <<'EOF'
Usage: install-binder.sh --dist DIR [--create-extension --dsn DSN --runtime-role ROLE] [--upgrade]

Installs only a package already present in DIR. It never downloads or uploads a
shared library. CREATE EXTENSION is opt-in and works only after server-side files
exist; managed RDS/Aurora/Azure/Cloud SQL custom C extensions are unsupported.
EOF
}

dist=
create=0
upgrade=0
dsn=
runtime_role=
while [ "$#" -gt 0 ]; do
  case "$1" in
    --dist) dist=$2; shift 2 ;;
    --create-extension) create=1; shift ;;
    --dsn) dsn=$2; shift 2 ;;
    --runtime-role) runtime_role=$2; shift 2 ;;
    --upgrade) upgrade=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) usage >&2; exit 64 ;;
  esac
done

test -n "$dist" && test -d "$dist" || { echo '--dist must name dist/binder' >&2; exit 64; }
command -v pg_config >/dev/null 2>&1 || { echo 'pg_config is required' >&2; exit 69; }
major=$(pg_config --version | sed -n 's/^PostgreSQL \([0-9][0-9]*\).*/\1/p')
case "$major" in 14|15|16|17|18) ;; *) echo "unsupported PostgreSQL major: $major" >&2; exit 65;; esac
machine=$(uname -m)
case "$machine" in x86_64|amd64) logical_arch=amd64; rpm_arch=x86_64; apk_arch=x86_64;; aarch64|arm64) logical_arch=arm64; rpm_arch=aarch64; apk_arch=aarch64;; *) echo "unsupported architecture: $machine" >&2; exit 65;; esac

if command -v dpkg >/dev/null 2>&1; then
  format=deb; package_arch=$logical_arch; suffix=deb
elif command -v rpm >/dev/null 2>&1; then
  format=rpm; package_arch=$rpm_arch; suffix=rpm
elif command -v apk >/dev/null 2>&1; then
  format=apk; package_arch=$apk_arch; suffix=apk
else
  echo 'no supported package manager found' >&2; exit 69
fi

package=$(find "$dist/$format/pg$major/$logical_arch" -maxdepth 1 -type f -name "agentsql-binder-pg$major*.$suffix" | head -1)
test -n "$package" || { echo "package not found for PG$major/$logical_arch/$format" >&2; exit 66; }
sbom="${package}.cdx.json"
test -f "$sbom" && test -f "$package.asc" && test -f "$sbom.asc" || { echo 'package, SBOM, or detached signature missing' >&2; exit 66; }
test -f "$dist/SHA256SUMS" && test -f "$dist/SHA256SUMS.asc" && test -f "$dist/DEV-ONLY-public-key.asc" || { echo 'signed checksum manifest or public key missing' >&2; exit 66; }
grep -q "\"agentsql:postgres-major\",\"value\":\"$major\"" "$sbom" || { echo 'SBOM PostgreSQL major mismatch' >&2; exit 65; }
grep -q "\"agentsql:architecture\",\"value\":\"$package_arch\"" "$sbom" || { echo 'SBOM architecture mismatch' >&2; exit 65; }

command -v gpg >/dev/null 2>&1 || { echo 'gpg is required for detached-signature verification' >&2; exit 69; }
verify_home=$(mktemp -d)
trap 'rm -rf "$verify_home"' EXIT INT TERM
chmod 0700 "$verify_home"
gpg --batch --homedir "$verify_home" --import "$dist/DEV-ONLY-public-key.asc" >/dev/null 2>&1
gpg --batch --homedir "$verify_home" --verify "$dist/SHA256SUMS.asc" "$dist/SHA256SUMS"
gpg --batch --homedir "$verify_home" --verify "$package.asc" "$package"
gpg --batch --homedir "$verify_home" --verify "$sbom.asc" "$sbom"
relative=${package#"$dist"/}
expected=$(awk -v path="$relative" '$2==path {print $1}' "$dist/SHA256SUMS")
actual=$(sha256sum "$package" | awk '{print $1}')
test -n "$expected" && test "$expected" = "$actual" || { echo 'SHA256 mismatch' >&2; exit 65; }

case "$format" in
  deb) dpkg -i "$package" ;;
  rpm) if [ "$upgrade" -eq 1 ]; then rpm -Uvh "$package"; else rpm -ivh "$package"; fi ;;
  apk) if [ "$upgrade" -eq 1 ]; then apk add --allow-untrusted --upgrade "$package"; else apk add --allow-untrusted "$package"; fi ;;
esac

test "$(pg_config --version | sed -n 's/^PostgreSQL \([0-9][0-9]*\).*/\1/p')" = "$major"
test -f "$(pg_config --sharedir)/extension/agentsql_binder.control"
test -f "$(pg_config --sharedir)/extension/agentsql_binder--0.4.sql"
test -f "$(pg_config --pkglibdir)/agentsql_binder-0.4.so"

if [ "$create" -eq 1 ]; then
  test -n "$dsn" && test -n "$runtime_role" || { echo '--dsn and --runtime-role are required with --create-extension' >&2; exit 64; }
  case "$runtime_role" in *[!A-Za-z0-9_]*|'') echo 'runtime role must be a simple PostgreSQL identifier' >&2; exit 64;; esac
  psql "$dsn" -X --no-psqlrc --set=ON_ERROR_STOP=1 --set=runtime_role="$runtime_role" <<'SQL'
SELECT pg_catalog.set_config('agentsql.bootstrap_runtime_role', :'runtime_role', false);
DO $agentsql_guard$
DECLARE
  runtime pg_catalog.pg_roles%ROWTYPE;
  bootstrap_is_super boolean;
BEGIN
  SELECT rolsuper INTO bootstrap_is_super FROM pg_catalog.pg_roles WHERE rolname=current_user;
  SELECT * INTO runtime FROM pg_catalog.pg_roles
    WHERE rolname=current_setting('agentsql.bootstrap_runtime_role');
  IF NOT FOUND OR NOT bootstrap_is_super OR runtime.rolsuper OR runtime.rolcreatedb OR runtime.rolcreaterole THEN
    RAISE EXCEPTION 'AgentSQL bootstrap/runtime privilege separation requirement failed';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_extension WHERE extname='agentsql_binder' AND extversion <> '0.4') THEN
    RAISE EXCEPTION 'installed agentsql_binder version is not 0.4';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_catalog.pg_namespace
             WHERE nspname='agentsql_catalog' AND nspowner <> (SELECT oid FROM pg_catalog.pg_roles WHERE rolname=current_user))
     AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_namespace n JOIN pg_catalog.pg_extension e ON e.extnamespace=n.oid
                     WHERE n.nspname='agentsql_catalog' AND e.extname='agentsql_binder' AND e.extversion='0.4') THEN
    RAISE EXCEPTION 'pre-existing agentsql_catalog schema is not owned by bootstrap role';
  END IF;
END
$agentsql_guard$;
CREATE SCHEMA IF NOT EXISTS agentsql_catalog;
CREATE EXTENSION IF NOT EXISTS agentsql_binder WITH SCHEMA agentsql_catalog VERSION '0.4';
REVOKE ALL ON SCHEMA agentsql_catalog FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA agentsql_catalog FROM PUBLIC;
GRANT USAGE ON SCHEMA agentsql_catalog TO :"runtime_role";
GRANT EXECUTE ON FUNCTION agentsql_catalog.capabilities(), agentsql_catalog.dml_capabilities(), agentsql_catalog.prepare(text,text), agentsql_catalog.prepare_dml(text,text), agentsql_catalog.seal_prepared(text), agentsql_catalog.prepared_manifest(text), agentsql_catalog.prepared_dml_manifest(text), agentsql_catalog.prepared_relations(text), agentsql_catalog.prepared_vars(text), agentsql_catalog.prepared_objects(text) TO :"runtime_role";
SELECT agentsql_catalog.capabilities()->>'extension_version' = '0.4' AS agentsql_binder_ready;
SQL
else
  echo 'Package installed. CREATE EXTENSION was not requested.'
fi
