#!/bin/sh
# Where does a request's time go? Runs a few read-only GETs against the API and
# splits each one into network (DNS, connect, TLS, the Cloudflare tunnel) and
# server time (from the Server-Timing header: database, TMDB, total).
#
#   sh deploy/speedtest.sh https://api.klyvi.net [rounds]
#
# Optional:
#   KLYVI_TOKEN=<access token>   also times your personal feed (/v1/reco/feed)
#   DB_ADDR=<host>:<port>        times a plain TCP connect to the database, which
#                                is about one round trip. Run this one ON the server,
#                                with the host and port from its .env.
#
# Run it from a laptop or phone hotspot and from the server itself. If both are
# slow, the time is in the API or the database. If only outside is slow, it's the
# tunnel or the network. Needs only sh, curl and awk.

set -u

base=${1:-}
rounds=${2:-5}
if [ -z "$base" ]; then
  echo "usage: sh deploy/speedtest.sh <api base url> [rounds]" >&2
  exit 2
fi
base=${base%/}

hdr=$(mktemp)
trap 'rm -f "$hdr"' EXIT

# Pull one dur= value out of a Server-Timing header. Empty when absent.
server_part() {
  tr -d '\r' < "$hdr" | awk -v key="$1" '
    tolower($0) ~ /^server-timing:/ {
      sub(/^[^:]*:[ ]*/, "")
      n = split($0, parts, ",")
      for (i = 1; i <= n; i++) {
        p = parts[i]; gsub(/^ +| +$/, "", p)
        if (index(p, key ";") == 1 || p == key) {
          if (match(p, /dur=[0-9.]+/)) print substr(p, RSTART + 4, RLENGTH - 4)
        }
      }
    }'
}

# Median of whitespace-separated numbers on stdin, or "-" when there are none.
median() {
  tr ' ' '\n' | awk 'NF' | sort -n | awk '
    { v[NR] = $1 }
    END {
      if (NR == 0) { print "-"; exit }
      if (NR % 2) m = v[(NR + 1) / 2]; else m = (v[NR / 2] + v[NR / 2 + 1]) / 2
      printf "%.0f\n", m
    }'
}

printf '%-22s %6s %6s %6s %6s %7s %6s %6s\n' \
  "endpoint (median ms)" connect tls first total server db net
failed=0

run() {
  label=$1
  path=$2
  auth=${3:-}
  conn=""; tls=""; ttfb=""; tot=""; srv=""; db=""; net=""; code=""
  i=0
  while [ "$i" -lt "$rounds" ]; do
    i=$((i + 1))
    if [ -n "$auth" ]; then
      out=$(curl -s -o /dev/null -D "$hdr" --max-time 20 -H "Authorization: Bearer $auth" \
        -w '%{http_code} %{time_connect} %{time_appconnect} %{time_starttransfer} %{time_total}' "$base$path")
    else
      out=$(curl -s -o /dev/null -D "$hdr" --max-time 20 \
        -w '%{http_code} %{time_connect} %{time_appconnect} %{time_starttransfer} %{time_total}' "$base$path")
    fi
    set -- $out
    code=${1:-000}
    case "$code" in 2??) ;; *) continue ;; esac
    conn="$conn $(awk -v s="$2" 'BEGIN { print s * 1000 }')"
    tls="$tls $(awk -v s="$3" 'BEGIN { print s * 1000 }')"
    first=$(awk -v s="$4" 'BEGIN { print s * 1000 }')
    ttfb="$ttfb $first"
    tot="$tot $(awk -v s="$5" 'BEGIN { print s * 1000 }')"
    s=$(server_part total)
    d=$(server_part db)
    if [ -n "$s" ]; then
      srv="$srv $s"
      net="$net $(awk -v f="$first" -v s="$s" 'BEGIN { print f - s }')"
    fi
    [ -n "$d" ] && db="$db $d"
  done
  if [ -z "$tot" ]; then
    failed=$((failed + 1))
    printf '%-22s failed, last status %s\n' "$label" "$code"
    return
  fi
  printf '%-22s %6s %6s %6s %6s %7s %6s %6s\n' "$label" \
    "$(echo "$conn" | median)" "$(echo "$tls" | median)" "$(echo "$ttfb" | median)" \
    "$(echo "$tot" | median)" "$(echo "$srv" | median)" "$(echo "$db" | median)" \
    "$(echo "$net" | median)"
}

run "health" "/health"
run "search" "/v1/search?query=dune"
run "movie page (cached)" "/v1/movies/550?id_type=tmdb"
if [ -n "${KLYVI_TOKEN:-}" ]; then
  run "your feed" "/v1/reco/feed" "$KLYVI_TOKEN"
else
  echo "your feed              skipped (set KLYVI_TOKEN to include it)"
fi

if [ -n "${DB_ADDR:-}" ]; then
  samples=""
  i=0
  while [ "$i" -lt "$rounds" ]; do
    i=$((i + 1))
    # curl holds a telnet connection open until --max-time, so keep it short. The
    # connect time is recorded either way.
    t=$(curl -s -o /dev/null --max-time 1 -w '%{time_connect}' "telnet://$DB_ADDR" < /dev/null)
    t=$(awk -v s="${t:-0}" 'BEGIN { print s * 1000 }')
    [ "$t" != "0" ] && samples="$samples $t"
  done
  rtt=$(echo "$samples" | median)
  printf '%-22s %6s ms, about one round trip\n' "database connect" "$rtt"
  if [ "$rtt" != "-" ]; then
    echo "  The feed makes up to 7 queries, some side by side, so up to $((rtt * 7)) ms of it can be round trips alone."
  else
    failed=$((failed + 1))
  fi
fi

cat <<'EOF'

connect  TCP connect to the nearest Cloudflare edge (or the host)
tls      TLS finished, counted from the start
first    first byte back, counted from the start
total    whole response
server   time inside the API (Server-Timing total), "-" if the API is older
db       database time inside the API
net      first minus server: DNS, TLS, the tunnel and the trip both ways
EOF

[ "$failed" -eq 0 ]
