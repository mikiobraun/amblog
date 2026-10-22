#!/bin/bash
# Local test network: three feed servers and one aggregator on localhost.
#
#   alice  amfeed  http://localhost:9100   member of the aggregator
#   bob    amfeed  http://localhost:9101   member of the aggregator
#   carol  amfeed  http://localhost:9102   not a member; alice follows her
#   agg    amagg   http://localhost:9200   "Dev Island", invite-only
#
# Ports are 9xxx so they don't clash with servers you run by hand (8100, 8200).
# Everything lives in data/dev (gitignored): configs, databases, logs, and an
# am config per person.
#
# Usage: scripts/dev.sh setup     build, create, start, log alice and bob in, post a little
#        scripts/dev.sh start     start the servers (after a stop)
#        scripts/dev.sh stop      stop the servers
#        scripts/dev.sh status    show which servers run
#        scripts/dev.sh reset     stop and delete data/dev
#        scripts/dev.sh am NAME ARGS...   run am as alice, bob or carol
#        scripts/dev.sh info      print URLs and tokens (e.g. for amfront's settings)
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
DEV=$ROOT/data/dev
FEEDS=(alice:9100 bob:9101 carol:9102)
AGG_PORT=9200

port_of() {
	for f in "${FEEDS[@]}"; do
		if [[ ${f%%:*} == "$1" ]]; then echo "${f##*:}"; return; fi
	done
	echo "unknown person $1 (alice, bob or carol)" >&2
	exit 1
}

build() { (cd "$ROOT" && go build -o . ./cmd/...); }

wait_for() {
	for _ in $(seq 50); do
		curl -s -o /dev/null "http://localhost:$1/" && return
		sleep 0.1
	done
	echo "server on :$1 did not come up, see $DEV/logs" >&2
	exit 1
}

start_one() { # name, command...
	local name=$1
	shift
	local pidfile=$DEV/pids/$name
	if [[ -f $pidfile ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then
		return
	fi
	"$@" >"$DEV/logs/$name.log" 2>&1 &
	echo $! >"$pidfile"
}

start() {
	[[ -d $DEV ]] || { echo "no dev setup; run: scripts/dev.sh setup" >&2; exit 1; }
	mkdir -p "$DEV/pids" "$DEV/logs"
	for f in "${FEEDS[@]}"; do
		start_one "${f%%:*}" "$ROOT/amfeed" serve -config "$DEV/${f%%:*}/amfeed.toml"
	done
	start_one agg "$ROOT/amagg" serve -config "$DEV/agg/amagg.toml"
	for f in "${FEEDS[@]}"; do wait_for "${f##*:}"; done
	wait_for $AGG_PORT
	status
}

stop() {
	for pidfile in "$DEV"/pids/*; do
		[[ -f $pidfile ]] || continue
		kill "$(cat "$pidfile")" 2>/dev/null || true
		rm "$pidfile"
	done
}

status() {
	for name in alice bob carol agg; do
		local pidfile=$DEV/pids/$name state=stopped
		if [[ -f $pidfile ]] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then state="running (pid $(cat "$pidfile"))"; fi
		printf "%-6s %s\n" "$name" "$state"
	done
}

am_as() { AM_CONFIG=$DEV/$1.toml "$ROOT/am" "${@:2}"; }

setup() {
	if [[ -d $DEV ]]; then
		echo "$DEV exists; use start, or reset first" >&2
		exit 1
	fi
	build
	mkdir -p "$DEV/pids" "$DEV/logs"
	for f in "${FEEDS[@]}"; do
		local name=${f%%:*} port=${f##*:}
		mkdir -p "$DEV/$name"
		"$ROOT/amfeed" init -dir "$DEV/$name" -base-url "http://localhost:$port" \
			-name "${name^}" -listen "localhost:$port" >"$DEV/$name/init.txt"
		local token
		token=$(grep -o 'amf_[A-Za-z0-9_-]*' "$DEV/$name/init.txt" | head -1)
		am_as "$name" config -feed "http://localhost:$port" -token "$token" >/dev/null
	done
	"$ROOT/amagg" init -dir "$DEV/agg" -base-url "http://localhost:$AGG_PORT" \
		-name "Dev Island" -listen "localhost:$AGG_PORT" >/dev/null
	start

	am_as alice post "Hello from Alice 👋" >/dev/null
	am_as bob post "Bob here. Testing the island." >/dev/null
	am_as carol post "Carol, outside the island, followed by Alice." >/dev/null
	for name in alice bob; do
		am_as "$name" login -invite "$("$ROOT/amagg" invite -config "$DEV/agg/amagg.toml")" "http://localhost:$AGG_PORT" >/dev/null
	done
	am_as alice follow http://localhost:9102/ >/dev/null
	echo
	info
}

info() {
	echo "Aggregator: http://localhost:$AGG_PORT  (island: http://localhost:$AGG_PORT/timeline.atom)"
	echo "New invite: $ROOT/amagg invite -config $DEV/agg/amagg.toml"
	echo
	for f in "${FEEDS[@]}"; do
		local name=${f%%:*}
		echo "$name: feed server http://localhost:${f##*:}"
		grep -E '^(token|agg_token|timeline_url)' "$DEV/$name.toml" | sed 's/^/  /'
	done
	echo
	echo "Run am as someone:  scripts/dev.sh am alice timeline"
}

case ${1:-} in
setup) setup ;;
start) start ;;
stop) stop ;;
status) status ;;
reset) stop; rm -rf "$DEV" ;;
info) info ;;
am)
	[[ $# -ge 2 ]] || { echo "usage: scripts/dev.sh am NAME ARGS..." >&2; exit 2; }
	port_of "$2" >/dev/null
	am_as "${@:2}"
	;;
*)
	sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
	exit 2
	;;
esac
