#!/usr/bin/env bash
# migrate.sh — Run *.up.sql migrations in order, tracking applied versions.
# Usage: ./scripts/migrate.sh [migrations_dir]
# Env vars: PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE
set -euo pipefail

MIGRATIONS_DIR="${1:-migrations}"

# Ensure the migrations table exists
psql -v ON_ERROR_STOP=1 <<'SQL'
CREATE TABLE IF NOT EXISTS schema_migrations (
    version TEXT PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
SQL

echo "Running migrations from: ${MIGRATIONS_DIR}"

for file in $(ls "${MIGRATIONS_DIR}"/*.up.sql 2>/dev/null | sort); do
    version=$(basename "${file}" .up.sql)

    already_applied=$(psql -t -A -c "SELECT COUNT(*) FROM schema_migrations WHERE version = '${version}';")
    if [ "${already_applied}" -eq 1 ]; then
        echo "  SKIP   ${version} (already applied)"
        continue
    fi

    echo "  APPLY  ${version}"
    psql -v ON_ERROR_STOP=1 -f "${file}"
    psql -v ON_ERROR_STOP=1 -c "INSERT INTO schema_migrations (version) VALUES ('${version}');"
    echo "  OK     ${version}"
done

echo "Migrations complete."
