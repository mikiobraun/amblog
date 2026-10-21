#!/bin/bash
# Regenerates amfront's test fixtures (src/lib/__fixtures__) from a real
# amfeed: a signed feed covering tricky content, and its key document.
# The TypeScript tests verify these, which checks that the Go and TypeScript
# signing implementations agree.
#
# Usage: amfront/scripts/gen-fixtures.sh   (from anywhere in the repo)
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OUT=$ROOT/amfront/src/lib/__fixtures__
PORT=8198
BASE=http://localhost:$PORT

TMP=$(mktemp -d)
export AM_CONFIG=$TMP/am.toml

(cd "$ROOT" && go build -o "$TMP" ./cmd/amfeed ./cmd/am)

"$TMP/amfeed" init -dir "$TMP/feed" -base-url $BASE -name Fixture -listen localhost:$PORT > "$TMP/init.txt"
TOKEN=$(grep -o 'amf_[A-Za-z0-9_-]*' "$TMP/init.txt" | head -1)
"$TMP/amfeed" serve -config "$TMP/feed/amfeed.toml" > "$TMP/serve.log" 2>&1 &
PID=$!
trap 'kill $PID; rm -rf "$TMP"' EXIT
for _ in $(seq 50); do curl -s $BASE/feed.atom > /dev/null && break; sleep 0.2; done

am() { "$TMP/am" "$@" > /dev/null; }
am config -feed $BASE -token "$TOKEN"
am post "Plain text with ümlauts, \"quotes\", <angle> & ampersand 🦫"
printf 'line one\r\nline two\\with backslash\n\n  indented' | am post -
am post -html -title "A long-form post" '<p>HTML <em>content</em></p>'
am reply $BASE/posts/1 "a reply"
am post "to be deleted"
am rm 5

mkdir -p "$OUT"
curl -s $BASE/feed.atom > "$OUT/feed.atom"
curl -s $BASE/.well-known/atomic-keys > "$OUT/keys.json"
echo "wrote $OUT"
