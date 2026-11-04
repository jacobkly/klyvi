#!/bin/sh
# Dumps the hosted database to the NAS as the read-only backup_job role
# (migration 00034), checks the file reads back, and keeps the newest dumps.
#
# Settings come from .env.backup next to this script, which git ignores. Put
# single quotes around the password:
#
#   BACKUP_DIR=/mnt/nas/klyvi-backups
#   PGHOST=aws-0-us-east-2.pooler.supabase.com
#   PGPORT=5432
#   PGUSER=backup_job.<project-ref>
#   PGPASSWORD='<the password set with ALTER ROLE>'
#   PGDATABASE=postgres
#
# Use Supabase's session pooler (port 5432), since pg_dump can't run through the
# transaction pooler. Optional settings: BACKUP_KEEP (14), PGSSLMODE (require) and
# PG_IMAGE (postgres:17-alpine, whose major version must not be older than the
# server's). An empty PG_IMAGE uses the pg_dump on PATH instead of Docker.
#
# Dumps hold email addresses and password hashes, so they are readable by their
# owner only. Live sign-in sessions and tokens are left out. README.md has the
# restore steps.

set -eu

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if [ -f "$here/.env.backup" ]; then
  set -a
  . "$here/.env.backup"
  set +a
fi

: "${BACKUP_DIR:?BACKUP_DIR is not set}"
: "${PGHOST:?PGHOST is not set}"
: "${PGUSER:?PGUSER is not set}"
: "${PGPASSWORD:?PGPASSWORD is not set}"
: "${PGDATABASE:?PGDATABASE is not set}"

BACKUP_KEEP=${BACKUP_KEEP:-14}
PG_IMAGE=${PG_IMAGE-postgres:17-alpine}
PGPORT=${PGPORT:-5432}
PGSSLMODE=${PGSSLMODE:-require}
export PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE PGSSLMODE

# At zero, pruning would delete every dump.
case "$BACKUP_KEEP" in
  '' | *[!0-9]*)
    echo "backup: BACKUP_KEEP must be a whole number of 1 or more" >&2
    exit 1
    ;;
esac
if [ "$BACKUP_KEEP" -lt 1 ]; then
  echo "backup: BACKUP_KEEP must be a whole number of 1 or more" >&2
  exit 1
fi

# Never created here: an unmounted NAS would otherwise fill the server's own disk.
if [ ! -d "$BACKUP_DIR" ]; then
  echo "backup: $BACKUP_DIR does not exist. Is the NAS mounted?" >&2
  exit 1
fi

# The password reaches Docker by name, so it never shows in a process list.
pg() {
  if [ -n "$PG_IMAGE" ]; then
    docker run --rm -i \
      -e PGHOST -e PGPORT -e PGUSER -e PGPASSWORD -e PGDATABASE -e PGSSLMODE \
      "$PG_IMAGE" "$@"
  else
    "$@"
  fi
}

umask 077

final="$BACKUP_DIR/klyvi-$(date -u +%Y%m%d-%H%M%S).dump"
partial="$final.partial"

# A killed run can leave one of these behind.
rm -f "$BACKUP_DIR"/klyvi-*.dump.partial
trap 'rm -f "$partial"' EXIT
trap 'exit 1' HUP INT TERM

pg pg_dump \
  --format=custom \
  --schema=public \
  --schema=auth \
  --exclude-table-data=auth.sessions \
  --exclude-table-data=auth.refresh_tokens \
  --exclude-table-data=auth.one_time_tokens \
  --exclude-table-data=auth.flow_state \
  --exclude-table-data=auth.mfa_amr_claims \
  --exclude-table-data=auth.mfa_challenges \
  --exclude-table-data=auth.saml_relay_states \
  --exclude-table-data=auth.audit_log_entries \
  >"$partial"

# --list reads only the table of contents, which survives a file cut off halfway,
# so that step only proves the users are in there. The full read proves the rest.
if ! pg pg_restore --list <"$partial" | grep -q 'TABLE DATA public users'; then
  echo "backup: the dump has no users table, so it was not kept" >&2
  exit 1
fi
if ! pg pg_restore --file=/dev/null <"$partial"; then
  echo "backup: the dump did not read back in full, so it was not kept" >&2
  exit 1
fi

mv "$partial" "$final"

# Pruned by count, and only after a good dump, so a run of failures never eats the
# last good backups. Names sort by date, and other files in the folder are ignored.
ls -1 "$BACKUP_DIR" |
  grep -E '^klyvi-[0-9]{8}-[0-9]{6}\.dump$' |
  sort -r |
  tail -n "+$((BACKUP_KEEP + 1))" |
  while IFS= read -r old; do
    rm -f -- "$BACKUP_DIR/$old"
  done

echo "backup: wrote $(basename "$final"), $(wc -c <"$final" | tr -d ' ') bytes"
