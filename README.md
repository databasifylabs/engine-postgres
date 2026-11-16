# Databasify PostgreSQL Engine

- [Introduction](#introduction)
- [Lines](#lines)
- [Features](#features)
- [Ports](#ports)
- [Running Locally](#running-locally)
- [Adding a Version Line](#adding-a-version-line)

## Introduction

The PostgreSQL engine for [Databasify](https://github.com/databasifylabs/databasify), built on the [engine SDK](https://github.com/databasifylabs/engine-sdk).

Each image is the official `postgres` image plus its tools and `databasify`, the [engine SDK](https://github.com/databasifylabs/engine-sdk) 1.0 runtime that runs as PID 1. Images are published as `ghcr.io/databasifylabs/databasify-postgres`.

Engine behaviour lives in the Databasify app: its PHP driver for this engine runs Bash scripts inside the container through the runtime. This repository changes only when the database or one of its tools moves to a new version.

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
| Host tuning | ✓ |

## Ports

| Port | Use |
| --- | --- |
| 25060 | `client`, for applications |
| 25061 | `pool`, PgBouncer |
| 8008, 2379, 2380 | `peers`: Patroni, and etcd client and peer traffic |

## Running Locally

`$IMAGES` is `tools/images.sh` in a clone of [engine-sdk](https://github.com/databasifylabs/engine-sdk) at the tag the `sdk` pin in `versions.json` names (`v1.0.0`). Run it from this repository's root:

```bash
$IMAGES check
$IMAGES build postgres-18 --load --tag smoke:postgres-18
$IMAGES smoke smoke:postgres-18 postgres
```

The smoke test starts the image, runs `images/postgres/smoke/check.sh` and then the SDK's runtime conformance suite.

## Adding a Version Line

Follow `.claude/skills/adding-a-version-line/SKILL.md`. It covers the `versions.json` entry, what differs per line, and the checks to run.
