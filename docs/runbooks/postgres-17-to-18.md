# PostgreSQL 17 To 18 Single-Host Upgrade

Use this runbook for the supported single-host Compose deployment. PostgreSQL
18 must start on new volumes and receive a verified logical PG17 restore. Do
not reuse, migrate in place, or delete the PG17 data and pgBackRest volumes.

## Container And Volume Contract

- `postgres` and `postgres-restore` use `hank-postgres:pg18`.
- `db-ops` uses `hank-db-ops:pg18` so its PostgreSQL client and pgBackRest
  tooling match the primary.
- The primary sets `PGDATA=/var/lib/postgresql/data` and mounts
  `hank_pg18_postgres_data` at `/var/lib/postgresql`.
- Restore tests use `hank_pg18_postgres_restore_data`; backups use
  `hank_pg18_pgbackrest_repo`.
- Primary, db-ops, and restore receive `PGBACKREST_REPO1_CIPHER_PASS` through
  their environment. Never pass the cipher on a command line.
- The db-ops runtime user must be able to traverse the deployed checkout, read
  `docker-compose.yml`, read its mode-`0600` env copy, and reach the Docker
  socket. The host `.env.cloud` stays mode `0600` and unreadable to that user.

The parent primary mount is deliberate. The upstream PostgreSQL 18 image
declares `/var/lib/postgresql` as a volume; mounting only its `data` child
creates an unwanted anonymous parent volume.

## Go/No-Go Sequence

1. Build both PG18 images and verify `postgres --version`, pgBackRest, pgvector,
   rendered Compose volumes, disk headroom, and exact PG17 rollback image IDs.
2. Stop agent, cloud, and db-ops. Require zero external client backends, issue
   a checkpoint, and capture table counts, extensions, database size, and the
   attachment SHA-256 manifest.
3. Create custom, schema-only, and globals dumps plus a SHA-256 manifest. Prove
   the custom dump in a disposable PG17 cluster with exact table-count parity
   and `pg_amcheck` before stopping PG17.
4. Start PG18 on fresh named volumes. Create and check the encrypted pgBackRest
   stanza as the `postgres` OS user, prove WAL archive, restore the verified
   logical dump, compare counts/extensions/migrations, and run `pg_amcheck`.
5. Run migration-up, strict status, deep schema drift, Hank health/auth/agent/
   file/attachment checks, and a transactional write/read smoke with rollback.
6. Request a new full backup through Hank's storage API, require its attachment
   archive, and restore-test that exact label into the PG18 restore volume.
7. Stop at the rollback boundary. Preserve the stopped PG17 volumes and tagged
   images until a separately approved cleanup after the PG18 soak.

Any failed integrity, restore, migration, schema, archive, authentication, or
attachment check is a stop condition.

## Rollback Boundary

Before an upgrade, tag the running PG17 images with immutable rollback tags
and preserve the PG17 Compose file. A rollback must not build images or use
`docker compose down -v`.

The deployment-specific evidence directory must contain exact tags, paths,
and commands. The command shape is:

```bash
# Stop the PG18 application and primary without deleting volumes.
cd /path/to/pg18-staging
docker compose --env-file .env.cloud -p hankserverside --profile agent stop agent cloud db-ops postgres

# Restore the exact PG17 image tags, then recreate from the preserved PG17
# checkout/Compose file so it mounts the untouched PG17 volumes.
docker image tag hank-postgres:pg17-rollback-YYYYMMDD hank-postgres:latest
docker image tag hank-db-ops:pg17-rollback-YYYYMMDD hank-db-ops:latest
cd /path/to/preserved-pg17-checkout
docker compose --env-file .env.cloud -p hankserverside up -d postgres
docker compose --env-file .env.cloud -p hankserverside --profile agent up -d cloud agent db-ops

curl -fsS http://127.0.0.1:18080/healthz
curl -fsS http://127.0.0.1:18080/readyz
scripts/doctor.sh
```

Document that writes accepted on PG18 after cutover are not present in the
frozen PG17 volume. Rollback therefore requires an explicit operator decision.
