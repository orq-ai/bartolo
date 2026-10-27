#!/bin/sh
# Run the official MCP conformance suite against a server bartolo generates.
#
#   conformance/run.sh [revision ...]     default: 2025-11-25 2026-07-28
#
# It generates conformance/openapi.yaml as an MCP-only project with the `mcp`
# block in conformance/mcp.json, serves it over Streamable HTTP on loopback with
# a stub upstream API, and runs each revision's required scenarios
# (`--requirements`) against conformance/expected-failures-<revision>.yaml.
# Every baselined check carries the reason it cannot pass; the run fails on any
# other failure, and on a baselined check that starts passing.
#
# Needs Go, Node.js (npx), and python3.
set -eu

CONFORMANCE_VERSION="${CONFORMANCE_VERSION:-0.2.0-alpha.12}"
PORT="${CONFORMANCE_PORT:-18998}"
UPSTREAM_PORT=18999

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REVISIONS="${*:-2025-11-25 2026-07-28}"
WORK="$(mktemp -d)"
SERVER_PID=""
UPSTREAM_PID=""
cleanup() {
	[ -n "$SERVER_PID" ] && kill "$SERVER_PID" 2>/dev/null || true
	[ -n "$UPSTREAM_PID" ] && kill "$UPSTREAM_PID" 2>/dev/null || true
	rm -rf "$WORK"
}
trap cleanup EXIT

echo "==> generating the fixture server"
go build -o "$WORK/bartolo" "$ROOT"
mkdir -p "$WORK/fixture"
cp "$ROOT/conformance/openapi.yaml" "$WORK/fixture/openapi.yaml"
cd "$WORK/fixture"
"$WORK/bartolo" init fixture --bartolo-path "$ROOT" --targets mcp >/dev/null
python3 -I - "$ROOT/conformance/mcp.json" <<'PY'
import json, sys
config = json.load(open('.bartolo.json'))
config['mcp'] = json.load(open(sys.argv[1]))
json.dump(config, open('.bartolo.json', 'w'), indent=2)
PY
"$WORK/bartolo" generate openapi.yaml >/dev/null 2>&1
cp "$ROOT/conformance/custom/register.go" cli/custom/register.go
go mod tidy >/dev/null 2>&1
go build -o "$WORK/server" ./cmd/fixture

echo "==> starting the stub upstream API and the server"
# Answers every API call with an empty object, so a tool call the suite makes
# reaches a real upstream rather than failing to connect.
python3 -I -c '
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def reply(self):
        n = int(self.headers.get("content-length") or 0)
        self.rfile.read(n)
        self.send_response(200)
        self.send_header("content-type", "application/json")
        self.end_headers()
        self.wfile.write(b"{\"id\":\"w1\",\"name\":\"widget\",\"data\":[]}")
    do_GET = do_POST = do_PUT = do_PATCH = do_DELETE = reply
HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
' "$UPSTREAM_PORT" &
UPSTREAM_PID=$!
FIXTURE_SERVER="http://127.0.0.1:$UPSTREAM_PORT" "$WORK/server" --http "127.0.0.1:$PORT" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 50); do
	curl --silent --fail "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 && break
	sleep 0.2
done
curl --silent --fail "http://127.0.0.1:$PORT/healthz" >/dev/null || { cat "$WORK/server.log"; exit 1; }

status=0
for revision in $REVISIONS; do
	echo "==> conformance $CONFORMANCE_VERSION, requirements $revision"
	baseline="$ROOT/conformance/expected-failures-$revision.yaml"
	if ! npx --yes "@modelcontextprotocol/conformance@$CONFORMANCE_VERSION" server \
		--url "http://127.0.0.1:$PORT/mcp" \
		--requirements "$revision" \
		--expected-failures "$baseline"; then
		status=1
	fi
done
exit "$status"
