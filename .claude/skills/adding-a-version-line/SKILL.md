---
name: adding-a-version-line
description: "Add a new PostgreSQL version line to this engine repository: the versions.json entry and its pinned tools, any per-line differences in the engine code, and the local build, smoke, conformance and capabilities check. Use when asked to support a new PostgreSQL major or minor release line. Do not use for patch bumps inside an existing line (the weekly refresh opens those)."
---

# Adding a PostgreSQL version line

A PostgreSQL line is a major (`18`). It takes the newest release of its major, and the base tag keeps its Debian suffix (`18.6-trixie`). The weekly refresh never moves a pin out of its line.

## The versions.json entry

1. Copy the newest `postgres-*` entry under `images` to the key `postgres-<line>`. Set `line`, `release` (the exact official tag), and `base.tag` to the official tag, keeping the suffix the other lines use.
2. Set `base.digest`. Get it from `docker buildx imagetools inspect <image>:<tag> --format '{{json .Manifest}}' | jq -r .digest`.
3. Set every package version to `"0"`, then run `$IMAGES refresh postgres-<line>`. It pins the packages and moves the base, copies, modules and sources to the newest patch of the line. It may also move the shared `go` digest: revert that unless you mean to rebuild every image.
4. Run `$IMAGES check`. It fails when a key isn't `<engine>-<line>`, when a pin left its line, or when `sdk` differs from `go.mod`.

## What differs per line in this engine

The image sets `DATABASIFY_LINE` from the line, and the engine reads it where releases differ:

- `DATABASIFY_LINE` names the binaries' directory, `/usr/lib/postgresql/<line>/bin`, in `engine/cluster.go` and `engine/archive.go` (`pg_controldata`, `postgres -C`). A new major only needs that directory to exist.
- `engine/catalog.go` builds the settings per line (`catalogFor`). For example, `autovacuum_max_workers` restarts the server before the major that reloads it (`reloadsAutovacuumSince`). Read the release notes for settings that change between restart and reload, or that are added or removed, and branch on the line with `version.NewerThan`.
- `engine/configure.go` passes the line to the config files and the process manifest. Check that pgBackRest and Patroni (installed from `packages`) and etcd (`copies.etcd`) support the new major.

Add or extend a Go test for each per-line branch you add.

## Check the new line

`$IMAGES` stands for `tools/images.sh` in a clone of [engine-sdk](https://github.com/databasifylabs/engine-sdk) at the SDK version this repository's `go.mod` requires. Run it from this repository's root:

```bash
go vet ./... && go test -race -count=1 ./...
$IMAGES check
$IMAGES build postgres-19 --load --tag smoke:postgres-19
$IMAGES smoke smoke:postgres-19 postgres
echo '{}' | docker run --rm -i --network none --entrypoint databasify smoke:postgres-19 engine capabilities | jq -S .
echo '{}' | docker run --rm -i --network none --entrypoint databasify smoke:postgres-19 config fields | jq -S .
```

- `smoke` runs `images/postgres/smoke/check.sh` against the new line, then the SDK's conformance suite.
- Compare `engine capabilities` and `config fields` with the closest existing line. Every difference should be one you meant, such as a password encryption or setting the new release adds.
- Also run the CI scan (`TRIVY_IMAGE` in the SDK's `.github/workflows/engine-images.yml`) on `smoke:postgres-19`.
- Put the results in the pull request with the line and OS you ran them on.

## The pull request

Open an issue first, branch from `main`, and open one pull request against `main` with the `versions.json` entry, any per-line code and tests, and the README's Lines section. The app registers the new line in its own repository ([databasifylabs/databasify](https://github.com/databasifylabs/databasify)), after this one merges.
