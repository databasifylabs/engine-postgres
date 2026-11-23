set -euo pipefail
ready() { pg_isready -q -h 127.0.0.1 -p 55432 -U postgres; }
for _ in $(seq 60); do ready && break; sleep 1; done
ready
psql -h 127.0.0.1 -p 55432 -U postgres -Atc 'SHOW server_version' | grep "^$DATABASIFY_RELEASE" > /dev/null
databasify ctl is-active --quiet server.service
[ "$(databasify ctl show -p NRestarts --value server)" = 0 ]
databasify ctl run-schedule probe
databasify ctl logs -u server -n 3
patroni --version
python3 -c 'import etcd'
pgbouncer --version | sed -n 1p
pgbackrest version
etcd --version | sed -n 1p
etcdctl version | sed -n 1p
echo "postgres $DATABASIFY_RELEASE runs under the databasify supervisor"
[ "$(id -u postgres)" = 64999 ] && [ "$(id -g postgres)" = 64999 ]
available=$(psql -h 127.0.0.1 -p 55432 -U postgres -Atc 'SELECT name FROM pg_available_extensions')
for extension in vector postgis postgis_raster postgis_topology pgrouting h3 timescaledb pg_cron pg_partman pg_repack pgaudit hypopg pg_hint_plan plpgsql_check pgtap rum age orafce mysql_fdw plperl pltcl pllua pljs; do
    grep -qx "$extension" <<< "$available" || { echo "missing extension $extension"; exit 1; }
done
[ -f "/usr/lib/postgresql/$DATABASIFY_LINE/lib/plugins/pgextwlist.so" ]
psql -h 127.0.0.1 -p 55432 -U postgres -Atc 'CREATE EXTENSION vector' > /dev/null
psql -h 127.0.0.1 -p 55432 -U postgres -Atc 'SELECT extversion FROM pg_extension WHERE extname = $$vector$$' | grep -E '^[0-9]' > /dev/null
echo "curated extensions are available"
