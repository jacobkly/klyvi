#!/bin/sh
# Tests for backup.sh, nightly.sh, speedtest.sh and licenses.sh, using stand-ins
# for pg_dump, pg_restore, docker and curl, and fake module folders. Touches nothing outside a temp folder. Run from anywhere:
#
#   sh deploy/scripts_test.sh

set -u

deploy=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

# A missing script exits non-zero, which the failure cases would count as a pass.
for script in backup.sh nightly.sh speedtest.sh licenses.sh; do
  if [ ! -f "$deploy/$script" ]; then
    echo "FAIL  $script does not exist"
    exit 1
  fi
done

root=$(mktemp -d)
trap 'rm -rf "$root"' EXIT

passed=0
failed=0

ok() {
  passed=$((passed + 1))
  echo "ok    $1"
}

bad() {
  failed=$((failed + 1))
  echo "FAIL  $1"
}

# A fresh folder per test: copies of the scripts (so a real .env.backup can never
# be read), stand-in commands on PATH, and an empty backup folder.
setup() {
  case_dir=$(mktemp -d "$root/case.XXXXXX")
  mkdir "$case_dir/bin" "$case_dir/nas"
  cp "$deploy/backup.sh" "$deploy/nightly.sh" "$case_dir/"
  : >"$case_dir/calls"

  cat >"$case_dir/bin/pg_dump" <<EOF
#!/bin/sh
echo "pg_dump \$*" >>"$case_dir/calls"
[ -f "$case_dir/dump_fails" ] && { echo "a good dump"; exit 1; }
[ -f "$case_dir/dump_garbage" ] && { echo garbage; exit 0; }
[ -f "$case_dir/dump_cut_short" ] && { echo "a good dump, cut short"; exit 0; }
echo "a good dump"
EOF

  # Like the real one, --list reads only the table of contents, so a dump cut short
  # gets past it and only a full read notices.
  cat >"$case_dir/bin/pg_restore" <<EOF
#!/bin/sh
echo "pg_restore \$*" >>"$case_dir/calls"
input=\$(cat)
case "\$input" in *"a good dump"*) ;; *) exit 1 ;; esac
case "\$*" in
  *--list*)
    if [ -f "$case_dir/no_users" ]; then
      echo "1234; 0 16385 TABLE DATA public movies postgres"
    else
      echo "1234; 0 16385 TABLE DATA public users postgres"
    fi
    ;;
  *)
    case "\$input" in *"cut short"*) exit 1 ;; esac
    ;;
esac
EOF

  cat >"$case_dir/bin/docker" <<EOF
#!/bin/sh
echo "docker \$*" >>"$case_dir/calls"
case "\$*" in
  *pg_dump*) echo "a good dump"; exit 0 ;;
  *pg_restore*) cat >/dev/null; echo "1234; 0 16385 TABLE DATA public users postgres"; exit 0 ;;
esac
for step in \$(cat "$case_dir/docker_fails" 2>/dev/null); do
  case "\$*" in *"\$step"*) exit 1 ;; esac
done
exit 0
EOF

  # nightly.sh pings its heartbeat with curl. The stand-in only records it.
  cat >"$case_dir/bin/curl" <<EOF
#!/bin/sh
echo "curl \$*" >>"$case_dir/calls"
EOF

  chmod +x "$case_dir/bin/"*
}

# Runs backup.sh with working settings, using the stand-ins directly.
run_backup() {
  PATH="$case_dir/bin:$PATH" \
    BACKUP_DIR="$case_dir/nas" \
    PG_IMAGE="" \
    PGHOST=db.test PGUSER=backup_job PGPASSWORD=not-a-secret PGDATABASE=postgres \
    "$@" sh "$case_dir/backup.sh" >"$case_dir/out" 2>&1
}

dumps() {
  ls -1 "$case_dir/nas" | grep -c '\.dump$'
}

# --- backup.sh ------------------------------------------------------------

setup
if run_backup env; then
  name=$(ls -1 "$case_dir/nas")
  if echo "$name" | grep -Eq '^klyvi-[0-9]{8}-[0-9]{6}\.dump$' && [ "$(cat "$case_dir/nas/$name")" = "a good dump" ]; then
    ok "a good run leaves one named dump and nothing else"
  else
    bad "a good run leaves one named dump and nothing else (found: $name)"
  fi
else
  bad "a good run succeeds: $(cat "$case_dir/out")"
fi

setup
run_backup env
if grep -q "pg_dump.*--format=custom" "$case_dir/calls" &&
  grep -q "pg_dump.*--schema=public" "$case_dir/calls" &&
  grep -q "pg_dump.*--schema=auth" "$case_dir/calls"; then
  ok "dumps public and auth in the custom format"
else
  bad "dumps public and auth in the custom format: $(cat "$case_dir/calls")"
fi

setup
run_backup env
missing=""
for table in sessions refresh_tokens one_time_tokens flow_state; do
  grep -q -- "--exclude-table-data=auth.$table" "$case_dir/calls" || missing="$missing $table"
done
if [ -z "$missing" ]; then
  ok "leaves live sign-in tokens out of the dump"
else
  bad "leaves live sign-in tokens out of the dump (kept:$missing)"
fi

setup
: >"$case_dir/nas/klyvi-20260101-000000.dump"
: >"$case_dir/dump_fails"
if run_backup env; then
  bad "a failed dump exits non-zero"
else
  ok "a failed dump exits non-zero"
fi
if [ "$(ls -1 "$case_dir/nas")" = "klyvi-20260101-000000.dump" ]; then
  ok "a failed dump leaves no partial file and keeps older dumps"
else
  bad "a failed dump leaves no partial file and keeps older dumps: $(ls -1 "$case_dir/nas")"
fi

setup
: >"$case_dir/dump_garbage"
if run_backup env; then
  bad "a dump that cannot be read back exits non-zero"
elif [ -z "$(ls -1 "$case_dir/nas")" ]; then
  ok "a dump that cannot be read back exits non-zero and is not kept"
else
  bad "a dump that cannot be read back is not kept: $(ls -1 "$case_dir/nas")"
fi

setup
: >"$case_dir/dump_cut_short"
if run_backup env; then
  bad "a dump cut short exits non-zero"
elif [ -z "$(ls -1 "$case_dir/nas")" ]; then
  ok "a dump cut short exits non-zero and is not kept"
else
  bad "a dump cut short is not kept: $(ls -1 "$case_dir/nas")"
fi

setup
: >"$case_dir/no_users"
if run_backup env; then
  bad "a dump with no users table exits non-zero"
elif [ -z "$(ls -1 "$case_dir/nas")" ]; then
  ok "a dump with no users table exits non-zero and is not kept"
else
  bad "a dump with no users table is not kept: $(ls -1 "$case_dir/nas")"
fi

setup
rm -r "$case_dir/nas"
if run_backup env; then
  bad "a missing backup folder exits non-zero"
elif [ -e "$case_dir/nas" ]; then
  bad "a missing backup folder is not created, an unmounted NAS must not fill the server"
else
  ok "a missing backup folder exits non-zero and is not created"
fi

setup
if PATH="$case_dir/bin:$PATH" BACKUP_DIR="$case_dir/nas" PG_IMAGE="" \
  sh "$case_dir/backup.sh" >"$case_dir/out" 2>&1; then
  bad "missing database settings exit non-zero"
elif [ -s "$case_dir/calls" ]; then
  bad "missing database settings stop before pg_dump"
else
  ok "missing database settings exit non-zero before pg_dump"
fi

setup
for day in 01 02 03 04 05; do
  : >"$case_dir/nas/klyvi-202601$day-030000.dump"
done
: >"$case_dir/nas/notes.txt"
: >"$case_dir/nas/klyvi-manual-copy.dump"
run_backup env BACKUP_KEEP=3
kept=$(ls -1 "$case_dir/nas" | grep -c '^klyvi-2026')
if [ "$kept" = "3" ] && [ ! -e "$case_dir/nas/klyvi-20260103-030000.dump" ] && [ -e "$case_dir/nas/klyvi-20260105-030000.dump" ]; then
  ok "keeps the newest BACKUP_KEEP dumps"
else
  bad "keeps the newest BACKUP_KEEP dumps: $(ls -1 "$case_dir/nas" | tr '\n' ' ')"
fi
if [ -e "$case_dir/nas/notes.txt" ] && [ -e "$case_dir/nas/klyvi-manual-copy.dump" ]; then
  ok "pruning touches only its own files"
else
  bad "pruning touches only its own files: $(ls -1 "$case_dir/nas" | tr '\n' ' ')"
fi

setup
: >"$case_dir/nas/klyvi-20260101-030000.dump"
for keep in 0 -1 many; do
  if run_backup env BACKUP_KEEP="$keep"; then
    bad "BACKUP_KEEP=$keep is refused"
  elif [ -e "$case_dir/nas/klyvi-20260101-030000.dump" ]; then
    ok "BACKUP_KEEP=$keep is refused and deletes nothing"
  else
    bad "BACKUP_KEEP=$keep deleted a dump"
  fi
done

setup
: >"$case_dir/nas/klyvi-20260101-030000.dump.partial"
run_backup env
if [ -e "$case_dir/nas/klyvi-20260101-030000.dump.partial" ]; then
  bad "clears a partial file left by a killed run"
else
  ok "clears a partial file left by a killed run"
fi

setup
if run_backup env && ! grep -q "not-a-secret" "$case_dir/out" "$case_dir/calls"; then
  ok "never prints or passes the password as an argument"
else
  bad "never prints or passes the password as an argument"
fi

setup
PATH="$case_dir/bin:$PATH" BACKUP_DIR="$case_dir/nas" PG_IMAGE="postgres:17-alpine" \
  PGHOST=db.test PGUSER=backup_job PGPASSWORD=not-a-secret PGDATABASE=postgres \
  sh "$case_dir/backup.sh" >"$case_dir/out" 2>&1
image_status=$?
if [ "$image_status" -eq 0 ] &&
  grep -q "docker run .*postgres:17-alpine pg_dump" "$case_dir/calls" &&
  grep -q "docker run .*postgres:17-alpine pg_restore --list" "$case_dir/calls" &&
  grep -q "docker run .*postgres:17-alpine pg_restore --file=/dev/null" "$case_dir/calls" &&
  grep -q -- "-e PGPASSWORD " "$case_dir/calls" &&
  ! grep -q "not-a-secret" "$case_dir/calls"; then
  ok "with PG_IMAGE it runs pg_dump in that image and hands the password over by name"
else
  bad "with PG_IMAGE it runs pg_dump in that image: $(cat "$case_dir/calls")"
fi

# --- nightly.sh -----------------------------------------------------------

run_nightly() {
  printf '%s\n' "${nightly_env:-}" >"$case_dir/.env"
  PATH="$case_dir/bin:$PATH" BACKUP_DIR="$case_dir/nas" PG_IMAGE="" \
    PGHOST=db.test PGUSER=backup_job PGPASSWORD=not-a-secret PGDATABASE=postgres \
    sh "$case_dir/nightly.sh" >"$case_dir/out" 2>&1
}

setup
if run_nightly; then
  order=$(grep -nE "seed-catalog|/notify|pg_dump " "$case_dir/calls" | cut -d: -f1 | tr '\n' ' ')
  refresh=$(grep -n -- "--entrypoint /seed-catalog klyvi-api -refresh-older-than 150d -timeout 2h" "$case_dir/calls" | cut -d: -f1)
  notify=$(grep -n -- "--entrypoint /notify klyvi-api" "$case_dir/calls" | cut -d: -f1)
  dump=$(grep -n "^pg_dump " "$case_dir/calls" | cut -d: -f1)
  if [ -n "$refresh" ] && [ -n "$notify" ] && [ -n "$dump" ] && [ "$refresh" -lt "$notify" ] && [ "$notify" -lt "$dump" ]; then
    ok "runs the refresh, then notices, then the backup"
  else
    bad "runs the refresh, then notices, then the backup (lines: $order)"
  fi
else
  bad "a good nightly run succeeds: $(cat "$case_dir/out")"
fi

setup
: >"$case_dir/.env"
if PATH="$case_dir/bin:$PATH" sh "$case_dir/nightly.sh" >"$case_dir/out" 2>&1 &&
  ! grep -q "^pg_dump " "$case_dir/calls" && grep -q "backup skipped" "$case_dir/out"; then
  ok "with no backup settings the backup is skipped and the run still succeeds"
else
  bad "with no backup settings the backup is skipped and the run still succeeds: $(cat "$case_dir/out")"
fi

setup
run_nightly
if grep "docker compose" "$case_dir/calls" | grep -qv "watchtower.enable=false"; then
  bad "job containers opt out of Watchtower, which would restart a long refresh"
else
  ok "job containers opt out of Watchtower"
fi

setup
echo "seed-catalog" >"$case_dir/docker_fails"
if run_nightly; then
  bad "a failed refresh makes the nightly run exit non-zero"
else
  ok "a failed refresh makes the nightly run exit non-zero"
fi
if grep -q "/notify" "$case_dir/calls" && grep -q "^pg_dump " "$case_dir/calls"; then
  ok "a failed refresh still runs notices and the backup"
else
  bad "a failed refresh still runs notices and the backup: $(cat "$case_dir/calls")"
fi

setup
echo "/notify" >"$case_dir/docker_fails"
if run_nightly; then
  bad "failed notices make the nightly run exit non-zero"
elif ls -1 "$case_dir/nas" | grep -q '\.dump$'; then
  ok "failed notices exit non-zero and the backup still lands"
else
  bad "failed notices must not stop the backup"
fi

setup
run_nightly
refresh=$(grep -n -- "--entrypoint /seed-catalog klyvi-api" "$case_dir/calls" | cut -d: -f1)
coliked=$(grep -n -- "--entrypoint /coliked klyvi-api" "$case_dir/calls" | cut -d: -f1)
notify=$(grep -n -- "--entrypoint /notify klyvi-api" "$case_dir/calls" | cut -d: -f1)
if [ -n "$refresh" ] && [ -n "$coliked" ] && [ -n "$notify" ] && [ "$refresh" -lt "$coliked" ] && [ "$coliked" -lt "$notify" ]; then
  ok "the co-liked counts rebuild after the refresh and before notices"
else
  bad "the co-liked counts rebuild after the refresh and before notices: $(cat "$case_dir/calls")"
fi

setup
echo "/coliked" >"$case_dir/docker_fails"
if run_nightly; then
  bad "failed co-liked counts make the nightly run exit non-zero"
elif grep -q "/notify" "$case_dir/calls" && ls -1 "$case_dir/nas" | grep -q '\.dump$'; then
  ok "failed co-liked counts exit non-zero and notices and the backup still run"
else
  bad "failed co-liked counts must not stop notices or the backup: $(cat "$case_dir/calls")"
fi

setup
NIGHTLY_WEEKDAY=7 run_nightly
notify=$(grep -n -- "--entrypoint /notify klyvi-api" "$case_dir/calls" | cut -d: -f1)
opendata=$(grep -n -- "--entrypoint /opendata klyvi-api -timeout 1h" "$case_dir/calls" | cut -d: -f1)
dump=$(grep -n "^pg_dump " "$case_dir/calls" | cut -d: -f1)
if [ -n "$opendata" ] && [ -n "$notify" ] && [ -n "$dump" ] && [ "$notify" -lt "$opendata" ] && [ "$opendata" -lt "$dump" ]; then
  ok "on Sundays the open-data refresh runs after notices and before the backup"
else
  bad "on Sundays the open-data refresh runs after notices and before the backup: $(cat "$case_dir/calls")"
fi

setup
NIGHTLY_WEEKDAY=7 run_nightly
opendata=$(grep -n -- "--entrypoint /opendata klyvi-api" "$case_dir/calls" | cut -d: -f1)
plots=$(grep -n -- "--entrypoint /plots klyvi-api -timeout 3h" "$case_dir/calls" | cut -d: -f1)
dump=$(grep -n "^pg_dump " "$case_dir/calls" | cut -d: -f1)
if [ -n "$plots" ] && [ -n "$opendata" ] && [ -n "$dump" ] && [ "$opendata" -lt "$plots" ] && [ "$plots" -lt "$dump" ]; then
  ok "on Sundays the plot vectors refresh after the open data and before the backup"
else
  bad "on Sundays the plot vectors refresh after the open data and before the backup: $(cat "$case_dir/calls")"
fi

setup
echo "/plots" >"$case_dir/docker_fails"
if NIGHTLY_WEEKDAY=7 run_nightly; then
  bad "failed plot vectors make the nightly run exit non-zero"
elif ls -1 "$case_dir/nas" | grep -q '\.dump$'; then
  ok "failed plot vectors exit non-zero and the backup still lands"
else
  bad "failed plot vectors must not stop the backup"
fi

setup
NIGHTLY_WEEKDAY=3 run_nightly
if grep -q "/opendata\|/plots" "$case_dir/calls"; then
  bad "the open-data and plot refreshes only run on Sundays"
else
  ok "the open-data and plot refreshes only run on Sundays"
fi

setup
nightly_env="HEARTBEAT_URL=https://hc.example.test/ping/abc"
run_nightly
nightly_env=""
if grep -q "^curl .*https://hc.example.test/ping/abc" "$case_dir/calls"; then
  ok "a clean run pings the heartbeat URL from .env"
else
  bad "a clean run pings the heartbeat URL from .env: $(cat "$case_dir/calls")"
fi

setup
nightly_env="HEARTBEAT_URL=https://hc.example.test/ping/abc"
echo "/notify" >"$case_dir/docker_fails"
run_nightly
nightly_env=""
if grep -q "^curl " "$case_dir/calls"; then
  bad "a failed step skips the heartbeat, so the missed ping raises the alarm"
else
  ok "a failed step skips the heartbeat, so the missed ping raises the alarm"
fi

setup
if run_nightly && ! grep -q "^curl " "$case_dir/calls"; then
  ok "with no heartbeat URL the ping is skipped and the run still succeeds"
else
  bad "with no heartbeat URL the ping is skipped and the run still succeeds: $(cat "$case_dir/out")"
fi

# --- speedtest.sh ----------------------------------------------------------

# A stand-in curl: answers with a fixed timing, the status in $case_dir/status
# (200 by default) and the Server-Timing header in $case_dir/timing, if any.
setup_speed() {
  case_dir=$(mktemp -d "$root/case.XXXXXX")
  mkdir "$case_dir/bin"
  cp "$deploy/speedtest.sh" "$case_dir/"
  : >"$case_dir/calls"
  cat >"$case_dir/bin/curl" <<EOF
#!/bin/sh
echo "curl \$*" >>"$case_dir/calls"
hdr=""; url=""
while [ \$# -gt 0 ]; do
  case "\$1" in
    -D) hdr=\$2; shift ;;
    -w|-o|-H|--max-time) shift ;;
    *) url=\$1 ;;
  esac
  shift
done
case "\$url" in telnet://*) printf '0.025'; exit 0 ;; esac
code=200
[ -f "$case_dir/status" ] && code=\$(cat "$case_dir/status")
{
  printf 'HTTP/2 %s\r\n' "\$code"
  [ -f "$case_dir/timing" ] && printf 'server-timing: %s\r\n' "\$(cat "$case_dir/timing")"
  printf '\r\n'
} >"\$hdr"
printf '%s 0.010 0.020 0.300 0.320' "\$code"
EOF
  chmod +x "$case_dir/bin/curl"
}

run_speed() {
  PATH="$case_dir/bin:$PATH" sh "$case_dir/speedtest.sh" https://api.example.test/ 3 >"$case_dir/out" 2>&1
}

setup_speed
echo 'db;desc="8 queries";dur=210.4, tmdb;desc="1 calls";dur=12, total;dur=250' >"$case_dir/timing"
if run_speed && grep -Eq '^health +10 +20 +300 +320 +250 +210 +50$' "$case_dir/out"; then
  ok "speedtest splits a request into server, database and network time"
else
  bad "speedtest should split server, database and network time: $(cat "$case_dir/out")"
fi

setup_speed
if run_speed && grep -Eq '^search +10 +20 +300 +320 +- +- +-$' "$case_dir/out"; then
  ok "speedtest still times an API without Server-Timing"
else
  bad "speedtest should show - for an API without Server-Timing: $(cat "$case_dir/out")"
fi

setup_speed
echo 503 >"$case_dir/status"
if run_speed; then
  bad "speedtest should exit non-zero when every request fails"
elif grep -q 'health .*failed, last status 503' "$case_dir/out"; then
  ok "speedtest reports failed requests and exits non-zero"
else
  bad "speedtest should name the failing status: $(cat "$case_dir/out")"
fi

setup_speed
if KLYVI_TOKEN=secret-token-123 run_speed && grep -q '^your feed ' "$case_dir/out" \
  && grep -q 'Authorization: Bearer secret-token-123 .*/v1/reco/feed' "$case_dir/calls" \
  && ! grep -q 'secret-token-123' "$case_dir/out"; then
  ok "speedtest times the feed with the token and never prints it"
else
  bad "speedtest should time the feed with the token, unprinted: $(cat "$case_dir/out")"
fi

setup_speed
if DB_ADDR=db.example.test:5432 run_speed && grep -Eq '^database connect +25 ms' "$case_dir/out" \
  && grep -q '175 ms of it' "$case_dir/out" && grep -q 'telnet://db.example.test:5432' "$case_dir/calls"; then
  ok "speedtest times the database connect and scales it to the feed"
else
  bad "speedtest should time the database connect: $(cat "$case_dir/out")"
fi

# --- licenses.sh -----------------------------------------------------------

# Fake module folders, as go list -deps would name them.
setup_lic() {
  case_dir=$(mktemp -d "$root/case.XXXXXX")
  mods="$case_dir/mods"
  mkdir -p "$mods/a" "$mods/b" "$mods/c/LICENSES"
  echo "A license text" >"$mods/a/LICENSE"
  echo "A notice text" >"$mods/a/NOTICE"
  echo "package a" >"$mods/a/a.go"
  echo "B licence text" >"$mods/b/LICENCE.md"
  echo "package c" >"$mods/c/c.go"
}

run_lic() {
  sh "$deploy/licenses.sh" "$case_dir/out" >"$case_dir/err" 2>&1
}

setup_lic
if printf '%s
' "example.com/b v1.0.0 $mods/b" "example.com/a v0.2.0 $mods/a"   "example.com/a v0.2.0 $mods/a" | run_lic   && [ "$(grep -c '^example.com/a v0.2.0$' "$case_dir/out")" -eq 1 ]   && grep -q 'A license text' "$case_dir/out" && grep -q 'A notice text' "$case_dir/out"   && grep -q 'B licence text' "$case_dir/out" && ! grep -q 'package a' "$case_dir/out"   && [ "$(grep -n '^example.com/a' "$case_dir/out" | cut -d: -f1)" -lt     "$(grep -n '^example.com/b' "$case_dir/out" | cut -d: -f1)" ]; then
  ok "licenses collects each module's license and notice once, in order"
else
  bad "licenses should collect each module's files once: $(cat "$case_dir/out" "$case_dir/err")"
fi

setup_lic
if echo "example.com/c v1.0.0 $mods/c" | run_lic   && grep -q 'No license file in the module.' "$case_dir/out"; then
  ok "licenses says when a module ships no license file"
else
  bad "licenses should note a module with no license file: $(cat "$case_dir/out" "$case_dir/err")"
fi

setup_lic
if echo "example.com/gone v1.0.0 $mods/gone" | run_lic; then
  bad "licenses should exit non-zero when a module folder is missing"
elif grep -q 'no folder for example.com/gone' "$case_dir/err"; then
  ok "licenses fails on a module it cannot find"
else
  bad "licenses should name the missing module: $(cat "$case_dir/err")"
fi

echo
echo "$passed passed, $failed failed"
[ "$failed" -eq 0 ]
