#!/bin/sh
# Runs once on the primary, when PostgreSQL creates an empty database
# (mounted into /docker-entrypoint-initdb.d next to schema.sql).
# It prepares the primary for a standby:
#   1. a separate user that may only replicate,
#   2. permission for that user to connect from the two database addresses.
set -e

psql -v ON_ERROR_STOP=1 -v pw="$REPLICATION_PASSWORD" \
     -U "$POSTGRES_USER" -d "$POSTGRES_DB" <<'SQL'
CREATE ROLE replicator WITH REPLICATION LOGIN PASSWORD :'pw';
SQL

cat >> "$PGDATA/pg_hba.conf" <<'HBA'

# Streaming replication between the two database nodes (stage 5).
host replication replicator 172.28.3.40/32 scram-sha-256
host replication replicator 172.28.3.41/32 scram-sha-256
HBA

echo "replication.sh: replication user and pg_hba rules created"
