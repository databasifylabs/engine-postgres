# Databasify PostgreSQL Engine

- [Introduction](#introduction)
- [Lines](#lines)
- [Features](#features)
- [Ports](#ports)
- [Engine Notes](#engine-notes)
- [Running Locally](#running-locally)
- [Adding a Version Line](#adding-a-version-line)

## Introduction

The PostgreSQL engine for [Databasify](https://github.com/databasifylabs/databasify), built on the [engine SDK](https://github.com/databasifylabs/engine-sdk).

Each image is the official `postgres` image plus `databasify`, the engine binary that runs as PID 1 and answers the SDK's typed commands. Images are published as `ghcr.io/databasifylabs/databasify-postgres`.

The commands and the flows Databasify runs are the same for every engine; see the SDK's [Flows](https://github.com/databasifylabs/engine-sdk#flows).

## Lines

PostgreSQL 15, 16, 17 and 18. Each line takes the newest release of its major and never leaves it. The weekly refresh moves the pins in `versions.json`.

## Features

| Feature | PostgreSQL |
| --- | --- |
| Node lifecycle, status, metrics, TLS | ✓ |
| Databases and users with read/write grants | ✓ |
| Password encryptions | — |
| High availability | Patroni with etcd |
| Automatic and manual failover, force quorum | ✓ |
| Standbys (up to 2) and read-only nodes (any number) | ✓ |
| Primary key required in a cluster | — |
| Connection pools | PgBouncer |
| Backups and point-in-time restore | pgBackRest |
| Continuous migration | logical replication |
| Fork (one-shot copy) | ✓ |
| Query insights | `pg_stat_statements` |
| Log forwarding and host tuning | ✓ |

A command the engine doesn't offer answers `unsupported`; `engine capabilities` lists the ones it does.

## Ports

| Port | Use |
| --- | --- |
| 25060 | `client`, for applications |
| 25061 | `pool`, PgBouncer |
| 8008, 2379, 2380 | `peers`: Patroni, and etcd client and peer traffic |

## Engine Notes

- To remove a node, `member forget` removes its etcd member.
- `config sync` pushes cluster-wide settings to Patroni.
- `node wake` with `all` starts every process.
- `migration start` requires `owner`, and a continuous migration also needs the source's `publication`.

### Connection Pools

Run each pool command on every node. `mode` is `session`, `transaction` or `statement`, and `size` is 1 to 1000.

```bash
echo '{"name":"shop_pool","database":"shop","size":20,"mode":"transaction"}' | databasify pool create
echo '{"name":"shop_pool","database":"shop","size":40,"mode":"session"}' | databasify pool update
echo '{}' | databasify pool list
echo '{"name":"shop_pool"}' | databasify pool delete
```

## Running Locally

`$IMAGES` is `tools/images.sh` in a clone of [engine-sdk](https://github.com/databasifylabs/engine-sdk) at the version `go.mod` requires. Run it from this repository's root:

```bash
go vet ./... && go test -race -count=1 ./...
$IMAGES build postgres-18 --load --tag smoke:postgres-18
$IMAGES smoke smoke:postgres-18 postgres
```

The smoke test starts the image, runs `images/postgres/smoke/check.sh` and then the SDK's conformance suite.

## Adding a Version Line

Follow `.claude/skills/adding-a-version-line/SKILL.md`. It covers the `versions.json` entry, the code that differs per line in this engine, and the checks to run.
