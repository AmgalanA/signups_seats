#!/bin/sh
# Start-up of the standby database.
#
# First start (empty data directory): take a full copy of the primary with
# pg_basebackup. The -R option also writes the settings that make this node
# a standby which keeps receiving every change from the primary.
# Later starts: the data is already there, just run PostgreSQL.
set -e

if [ ! -s "$PGDATA/PG_VERSION" ]; then
    echo "standby: empty data directory, waiting for the primary at $PRIMARY_HOST"
    until pg_isready -q -h "$PRIMARY_HOST" -p 5432; do
        sleep 1
    done

    echo "standby: copying the primary"
    mkdir -p "$PGDATA"
    chown postgres:postgres "$PGDATA"
    chmod 700 "$PGDATA"
    su-exec postgres pg_basebackup \
        -d "host=$PRIMARY_HOST port=5432 user=replicator password=$REPLICATION_PASSWORD" \
        -D "$PGDATA" -R -X stream -c fast
    echo "standby: copy finished"
fi

exec docker-entrypoint.sh "$@"
