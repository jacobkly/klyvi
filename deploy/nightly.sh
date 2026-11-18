#!/bin/sh
# The one nightly cron entry: catalog refresh, then the co-liked counts, then
# notices, then on Sundays the open-data refresh from Wikidata and the plot
# vectors from Wikipedia, then the backup when one is set up.
#
#   30 3 * * * sh /path/to/deploy/nightly.sh > /var/log/klyvi-nightly.log 2>&1
#
# The jobs run from the deployed API image, so they always match the live API, with
# the same .env and memory limit as the klyvi-api service. Each step runs even when
# the one before it failed, since a failed refresh is no reason to skip a backup.
# The run exits non-zero if any step failed.

set -u

here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
failed=0

step() {
  name=$1
  shift
  echo "== $(date -u +%H:%M:%S) $name"
  if ! "$@"; then
    echo "== $name FAILED"
    failed=1
  fi
}

# Watchtower restarts any labelled container when a new image lands, which would
# rerun a job from the start. The label is off here for that reason.
job() {
  entrypoint=$1
  shift
  docker compose -f "$here/docker-compose.yml" run --rm -T \
    --label com.centurylinklabs.watchtower.enable=false \
    --entrypoint "$entrypoint" klyvi-api "$@"
}

step "catalog refresh" job /seed-catalog -refresh-older-than 150d -timeout 2h
step "co-liked counts" job /coliked
step "notices" job /notify
# Wikidata changes slowly, so its facts refresh once a week (day 7 is Sunday, UTC).
# The plot vectors follow, since they read the Wikipedia links the facts hold.
# Only changed articles are embedded, so after the first run this is quick.
if [ "${NIGHTLY_WEEKDAY:-$(date -u +%u)}" = 7 ]; then
  step "open data" job /opendata -timeout 1h
  step "plot vectors" job /plots -timeout 3h
fi
# Backups are optional. With no .env.backup next to this script and no BACKUP_DIR
# set, the step is skipped rather than failing every night.
if [ -f "$here/.env.backup" ] || [ -n "${BACKUP_DIR:-}" ]; then
  step "backup" sh "$here/backup.sh"
else
  echo "== backup skipped, no .env.backup"
fi

echo "== $(date -u +%H:%M:%S) done, failed=$failed"
exit "$failed"
