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
echo "postgres $DATABASIFY_RELEASE answered through the wrapper"
[ "$(id -u postgres)" = 64999 ] && [ "$(id -g postgres)" = 64999 ]
