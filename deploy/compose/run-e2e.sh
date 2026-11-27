#!/usr/bin/env bash
# End-to-end test of the packaged link server: the image from
# deploy/docker/Dockerfile, two replicas on one S3 bucket (versitygw), and a
# TLS proxy (Caddy) in front. Clients run on the host with a host-built prism.
#
#   deploy/compose/run-e2e.sh            # build, start, test, tear down
#   KEEP_STACK=1 deploy/compose/run-e2e.sh   # leave the stack and .e2e up
#
# Environment:
#   PRISM_BIN   host prism binary (default: built from this checkout)
#   E2E_DIR     work directory (default: deploy/compose/.e2e)
#   SKIP_BUILD  1 = reuse the prism-e2e-image:dev image
#
# Prints PASS/FAIL per check; exits non-zero if any check fails. A check
# marked KNOWN-BUG documents a product bug with a workaround in place (see
# README.md) and does not fail the run.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../.." && pwd)"
E2E_DIR="${E2E_DIR:-$HERE/.e2e}"
export E2E_DIR
PROJECT=prism-e2e
IMAGE=prism-e2e-image:dev
export PRISM_IMAGE=$IMAGE
S3_ACCESS_KEY=prism-e2e-key
S3_SECRET_KEY=prism-e2e-secret-0123456789
BUCKET=prism-e2e
S3_URL=http://127.0.0.1:17070
PROXY=https://localhost:18443
LSA=http://127.0.0.1:18891
LSB=http://127.0.0.1:18892
LSA_OPS=http://127.0.0.1:19891
LSB_OPS=http://127.0.0.1:19892
RATE=https://localhost:18893
RATE_OPS=http://127.0.0.1:19893
APP=example.com/acme/app
SVC=example.com/acme/svc

compose() { docker compose -p "$PROJECT" -f "$HERE/docker-compose.yml" "$@"; }

PASSES=0 FAILS=0 KNOWN=0
RESULTS=()
pass() { PASSES=$((PASSES + 1)); RESULTS+=("PASS  $1"); echo "PASS  $1"; }
fail() { FAILS=$((FAILS + 1)); RESULTS+=("FAIL  $1${2:+ -- $2}"); echo "FAIL  $1${2:+ -- $2}"; }
known() { KNOWN=$((KNOWN + 1)); RESULTS+=("KNOWN-BUG  $1 -- $2"); echo "KNOWN-BUG  $1 -- $2"; }
check() { # check NAME CMD...: PASS when CMD succeeds
	local name=$1; shift
	if "$@"; then pass "$name"; else fail "$name"; fi
}
log() { echo "---- $*"; }

cleanup() {
	local rc=$?
	if [ "${KEEP_STACK:-0}" = 1 ]; then
		echo "KEEP_STACK=1: stack, $E2E_DIR and ${REPOS_DIR:-} left in place (tear down: docker compose -p $PROJECT down -v; docker rm -f prism-e2e-rate)"
		return
	fi
	log "tearing down"
	docker rm -f prism-e2e-rate >/dev/null 2>&1 || true
	compose down -v --remove-orphans >/dev/null 2>&1 || true
	rm -rf "$E2E_DIR" "${REPOS_DIR:-}"
	exit $rc
}
trap cleanup EXIT
trap 'exit 130' INT TERM

for tool in docker openssl curl python3 git; do
	command -v "$tool" >/dev/null || { echo "missing $tool"; exit 2; }
done

rm -rf "$E2E_DIR"
mkdir -p "$E2E_DIR/tokens" "$E2E_DIR/certs"
# The test repositories live outside this checkout: a repository nested in an
# indexed checkout is exported as the outer one (see README.md, Known issues).
REPOS_DIR=$(mktemp -d "${TMPDIR:-/tmp}/prism-e2e-repos.XXXXXX")

# ---------------------------------------------------------------- binaries
if [ -z "${PRISM_BIN:-}" ]; then
	log "building host prism"
	(cd "$ROOT" && GOWORK=off go build -o "$E2E_DIR/prism" ./cmd/prism) || { echo "host build failed"; exit 2; }
	PRISM_BIN="$E2E_DIR/prism"
fi
P="$PRISM_BIN"
if [ "${SKIP_BUILD:-0}" != 1 ]; then
	log "building $IMAGE"
	docker build -q -f "$ROOT/deploy/docker/Dockerfile" --build-arg VERSION=e2e -t "$IMAGE" "$ROOT" >/dev/null || { echo "image build failed"; exit 2; }
fi
check "image runs as non-root" test "$(docker image inspect "$IMAGE" --format '{{.Config.User}}')" = "65532:65532"
check "image prints link-server help by default" bash -c "docker run --rm --name prism-e2e-help $IMAGE | grep -q 'prism link-server token create'"

# ---------------------------------------------------------------- certificates
log "generating a throwaway CA, server and client certificates"
C="$E2E_DIR/certs"
{
	openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=prism-e2e CA" \
		-keyout "$C/ca.key" -out "$C/ca.crt" \
		-addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign"
	openssl req -newkey rsa:2048 -nodes -subj "/CN=localhost" -keyout "$C/server.key" -out "$C/server.csr"
	printf 'subjectAltName=DNS:localhost,IP:127.0.0.1\nextendedKeyUsage=serverAuth\n' >"$C/server.ext"
	openssl x509 -req -in "$C/server.csr" -CA "$C/ca.crt" -CAkey "$C/ca.key" -CAcreateserial -days 2 \
		-extfile "$C/server.ext" -out "$C/server.crt"
	openssl req -newkey rsa:2048 -nodes -subj "/CN=prism-e2e client" -keyout "$C/client.key" -out "$C/client.csr"
	printf 'extendedKeyUsage=clientAuth\n' >"$C/client.ext"
	openssl x509 -req -in "$C/client.csr" -CA "$C/ca.crt" -CAkey "$C/ca.key" -CAcreateserial -days 2 \
		-extfile "$C/client.ext" -out "$C/client.crt"
} >/dev/null 2>&1 || { echo "openssl failed"; exit 2; }
# The containers run as other users than this one; this directory is throwaway.
chmod 0755 "$C"; chmod 0644 "$C"/*

# ---------------------------------------------------------------- tokens
T="$E2E_DIR/tokens/tokens.json"
# mint NAME ARGS...: prints the secret; the id goes to $E2E_DIR/NAME.id
mint() {
	local name=$1; shift
	local err="$E2E_DIR/$name.err" out
	out=$("$P" link-server token create --tokens "$T" --name "$name" "$@" 2>"$err") || true
	sed -n 's/^created token \([0-9a-f]*\) .*/\1/p' "$err" >"$E2E_DIR/$name.id"
	echo "$out"
}
ADMIN=$(mint admin --admin --upload '*' --read '*')
CI=$(mint ci-app --upload "$APP")
READER=$(mint reader --read 'example.com/acme/*')
OUTSIDER=$(mint outsider --read 'other.org/*')
TEMP=$(mint temp --read '*')
pass "prism link-server token create"
for s in "$ADMIN" "$CI" "$READER" "$OUTSIDER" "$TEMP"; do
	case "$s" in plk_*) ;; *) echo "token mint failed: $s"; cat "$E2E_DIR"/*.err; exit 2 ;; esac
done
check "token list shows 5 tokens, no secrets" bash -c "out=\$('$P' link-server token list --tokens '$T'); [ \$(echo \"\$out\" | wc -l) -eq 6 ] && ! echo \"\$out\" | grep -q plk_"
SECRETS=("$ADMIN" "$CI" "$READER" "$OUTSIDER" "$TEMP")

# ---------------------------------------------------------------- stack
log "starting S3 (versitygw)"
compose up -d s3 >/dev/null 2>&1 || { echo "s3 failed to start"; exit 2; }
for _ in $(seq 1 50); do
	curl -s -o /dev/null "$S3_URL/" && break
	sleep 0.2
done
s3() { curl -s --aws-sigv4 "aws:amz:us-east-1:s3" --user "$S3_ACCESS_KEY:$S3_SECRET_KEY" "$@"; }
code=$(s3 -o /dev/null -w '%{http_code}' -X PUT "$S3_URL/$BUCKET")
check "S3 bucket created" test "$code" = 200

log "starting two link-server replicas and the TLS proxy"
compose up -d --no-build lsa lsb proxy >/dev/null 2>&1 || { compose logs; echo "stack failed to start"; exit 2; }
wait_http() { # wait_http URL [curl args]: up to 15 s for a 200
	local url=$1; shift
	for _ in $(seq 1 75); do
		[ "$(curl -s -o /dev/null -w '%{http_code}' "$@" "$url")" = 200 ] && return 0
		sleep 0.2
	done
	return 1
}
check "replica A ops /health" wait_http "$LSA_OPS/health"
check "replica B ops /health" wait_http "$LSB_OPS/health"
check "proxy /health over TLS (private CA)" wait_http "$PROXY/health" --cacert "$C/ca.crt"
if [ $FAILS -gt 0 ]; then compose logs --tail 30; exit 1; fi

# ---------------------------------------------------------------- repositories
G() { git -c user.email=e2e@example.com -c user.name=e2e -c commit.gpgsign=false "$@"; }
R="$REPOS_DIR"
mkdir -p "$R/lib" "$R/app" "$R/svc"
(
	cd "$R/lib"
	printf 'module example.com/acme/lib\n\ngo 1.22\n' >go.mod
	printf 'package lib\n\n// Normalize is the symbol the consumers use.\nfunc Normalize(s string) string { return s }\n' >lib.go
	G init -q -b main && G add -A && G commit -qm lib
) || exit 2
for r in app svc; do
	(
		cd "$R/$r"
		printf 'module example.com/acme/%s\n\ngo 1.22\n\nrequire example.com/acme/lib v0.1.0\n\nreplace example.com/acme/lib => ../lib\n' "$r" >go.mod
		printf 'package main\n\nimport "example.com/acme/lib"\n\nfunc main() { println(lib.Normalize("%s")) }\n' "$r" >main.go
		G init -q -b main && G add -A && G commit -qm "$r" && "$P" index . >/dev/null 2>&1
	) || { echo "repo $r setup failed"; exit 2; }
done

push() { # push DIR REPO SERVER TOKEN [env...]
	local dir=$1 repo=$2 server=$3 tok=$4; shift 4
	(cd "$dir" && env PRISM_LINK_TOKEN="$tok" "$@" "$P" workspace push --server "$server" --repo "$repo" --ref main 2>&1)
}
repos_on() { # repos_on URL TOKEN: the repo names a replica serves
	curl -s -H "Authorization: Bearer $2" "$1/link/repos" | python3 -c 'import json,sys; print("\n".join(r["repo"] for r in json.load(sys.stdin)["repos"]))'
}

# ---------------------------------------------------------------- uploads and scopes
log "uploads and token scopes"
out=$(push "$R/app" "$APP" "$PROXY" "$CI")
if ! echo "$out" | grep -q "^pushed" && echo "$out" | grep -qi "certificate"; then pass "push without PRISM_LINK_CA refuses the private CA"; else fail "push without PRISM_LINK_CA refuses the private CA" "$out"; fi
out=$(push "$R/app" "$APP" "$PROXY" "$CI" PRISM_LINK_CA="$C/ca.crt")
if echo "$out" | grep -q "^pushed $APP@"; then pass "CI token push via TLS proxy with PRISM_LINK_CA"; else fail "CI token push via TLS proxy with PRISM_LINK_CA" "$out"; fi
export PRISM_LINK_CA="$C/ca.crt"
out=$(push "$R/svc" "$SVC" "$PROXY" "$CI")
if echo "$out" | grep -q "403"; then pass "CI token pushing another repository is 403"; else fail "CI token pushing another repository is 403" "$out"; fi
code=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$C/ca.crt" "$PROXY/link/repos")
check "no token is 401" test "$code" = 401
code=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$C/ca.crt" -H "Authorization: Bearer plk_000000000000_bogus" "$PROXY/link/repos")
check "unknown token is 401" test "$code" = 401

# ---------------------------------------------------------------- replicas
log "replica A upload seen by replica B"
out=$(push "$R/svc" "$SVC" "$LSA" "$ADMIN")
if echo "$out" | grep -q "^pushed $SVC@"; then pass "admin push straight to replica A"; else fail "admin push straight to replica A" "$out"; fi
check "replica A serves its own upload at once" bash -c "curl -s -H 'Authorization: Bearer $ADMIN' $LSA/link/repos | grep -q '\"$SVC\"'"
start=$(python3 -c 'import time; print(time.time())')
seen=""
for _ in $(seq 1 40); do
	if repos_on "$LSB" "$ADMIN" | grep -qx "$SVC"; then
		seen=$(python3 -c "import time; print('%.1f' % (time.time() - $start))")
		break
	fi
	sleep 0.25
done
if [ -n "$seen" ] && python3 -c "import sys; sys.exit(0 if $seen <= 5 else 1)"; then
	pass "replica B serves replica A's upload after ${seen}s (--refresh 2s)"
else
	fail "replica B serves replica A's upload within 5s" "seen=${seen:-never}"
fi

# ---------------------------------------------------------------- reads
log "change-impact through the server"
printf 'workspace_server: %s\n' "$PROXY" >"$R/lib/prism.yaml"
(cd "$R/lib" && "$P" index . >/dev/null 2>&1)
out=$(cd "$R/lib" && PRISM_LINK_TOKEN="$READER" "$P" change-impact Normalize --format text 2>&1)
if echo "$out" | grep -q "$APP:main.go:5:" && echo "$out" | grep -q "$SVC:main.go:5:"; then
	pass "reader change-impact lists app and svc sites from the server"
else
	fail "reader change-impact lists app and svc sites from the server" "$(echo "$out" | head -20)"
fi
out=$(cd "$R/lib" && PRISM_LINK_TOKEN="$OUTSIDER" "$P" change-impact Normalize --format text 2>&1)
if echo "$out" | grep -q "NOT READABLE" && ! echo "$out" | grep -q "$APP:main.go"; then
	pass "token without read scope: NOT READABLE, no sites"
else
	fail "token without read scope: NOT READABLE, no sites" "$(echo "$out" | grep -i workspaceScope)"
fi

# ---------------------------------------------------------------- revocation
log "revocation"
c1=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TEMP" "$LSA/link/repos")
check "temp token works before revoke" test "$c1" = 200
"$P" link-server token revoke --tokens "$T" "$(cat "$E2E_DIR/temp.id")" >/dev/null
start=$(python3 -c 'import time; print(time.time())')
gone=""
for _ in $(seq 1 40); do
	a=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TEMP" "$LSA/link/repos")
	b=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TEMP" "$LSB/link/repos")
	if [ "$a" = 401 ] && [ "$b" = 401 ]; then
		gone=$(python3 -c "import time; print('%.1f' % (time.time() - $start))")
		break
	fi
	sleep 0.1
done
if [ -n "$gone" ] && python3 -c "import sys; sys.exit(0 if $gone <= 3 else 1)"; then
	pass "revoked token refused by both replicas after ${gone}s"
else
	fail "revoked token refused by both replicas within 3s" "last codes A=$a B=$b"
fi
c2=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $READER" "$LSA/link/repos")
check "other tokens still work after the revoke" test "$c2" = 200

# ---------------------------------------------------------------- retention
log "retention (--keep 2)"
ok=1
for i in 1 2 3 4 5; do
	(cd "$R/app" && echo "// rev $i" >>main.go && G commit -qam "rev $i" && "$P" index . >/dev/null 2>&1) || ok=0
	out=$(push "$R/app" "$APP" "$PROXY" "$CI")
	echo "$out" | grep -q "^pushed $APP@" || { ok=0; echo "  push $i: $out"; }
done
check "5 more commits of app pushed via the proxy" test $ok = 1
HEAD_SHA=$(cd "$R/app" && G rev-parse HEAD)
keys=$(s3 "$S3_URL/$BUCKET?list-type=2&prefix=link/" | python3 -c '
import re,sys; print("\n".join(re.findall(r"<Key>([^<]+)</Key>", sys.stdin.read())))')
app_bundles=$(echo "$keys" | grep -c '^link/example.com~acme~app/[0-9a-f]\{40\}\.json$')
echo "  S3 objects under link/:"; echo "$keys" | sed 's/^/    /'
check "app keeps 2 bundles in S3 after 6 uploads" test "$app_bundles" = 2
check "the branch head's bundle is kept" bash -c "echo '$keys' | grep -q 'link/example.com~acme~app/$HEAD_SHA.json'"
out=$(AWS_ACCESS_KEY_ID=$S3_ACCESS_KEY AWS_SECRET_ACCESS_KEY=$S3_SECRET_KEY "$P" link-server check --s3 "s3://$BUCKET/link" --s3-endpoint "$S3_URL" --s3-path-style 2>&1 </dev/null)
rc=$?
echo "  check: $out"
if [ $rc = 0 ] && echo "$out" | grep -q "^2 repositories, 3 bundles, 0 problems"; then
	pass "prism link-server check --s3: 2 repositories, 3 bundles, 0 problems"
else
	fail "prism link-server check --s3" "rc=$rc $out"
fi
# Uploads alternate between the replicas, so the one that did not take the
# last upload answers with its own last commit until its next refresh (2s).
# Both answers through the proxy must name the head within one refresh.
start=$(python3 -c 'import time; print(time.time())')
conv=""
for _ in $(seq 1 20); do
	a1=$(cd "$R/lib" && PRISM_LINK_TOKEN="$READER" "$P" change-impact Normalize --format text 2>&1 | grep "// repo $APP@")
	a2=$(cd "$R/lib" && PRISM_LINK_TOKEN="$READER" "$P" change-impact Normalize --format text 2>&1 | grep "// repo $APP@")
	if echo "$a1" | grep -q "@${HEAD_SHA:0:8} " && echo "$a2" | grep -q "@${HEAD_SHA:0:8} "; then
		conv=$(python3 -c "import time; print('%.1f' % (time.time() - $start))")
		break
	fi
	sleep 0.25
done
if [ -n "$conv" ] && python3 -c "import sys; sys.exit(0 if $conv <= 4 else 1)"; then
	pass "both replicas answer with the app's head commit after ${conv}s"
else
	fail "both replicas answer with the app's head commit within 4s" "want ${HEAD_SHA:0:8}: $a1 / $a2"
fi

# ---------------------------------------------------------------- audit log
log "audit log"
audit=$( { docker logs prism-e2e-lsa 2>/dev/null; docker logs prism-e2e-lsb 2>/dev/null; } | grep '"endpoint"')
n=$(echo "$audit" | grep -c '"endpoint"')
check "audit lines exist on both replicas ($n lines)" bash -c "docker logs prism-e2e-lsa 2>/dev/null | grep -q '\"endpoint\"' && docker logs prism-e2e-lsb 2>/dev/null | grep -q '\"endpoint\"'"
check "audit records the CI upload with token name and repo" bash -c "echo '$audit' | grep '\"endpoint\":\"upload\"' | grep '\"tokenName\":\"ci-app\"' | grep -q '\"repo\":\"$APP\"'"
check "audit records the 403" bash -c "echo '$audit' | grep '\"status\":403' | grep -q '\"repo\":\"$SVC\"'"
check "audit records 401s with no token id" bash -c "echo '$audit' | grep '\"status\":401' | grep -vq tokenId"
leak=0
alllogs=$( { docker logs prism-e2e-lsa; docker logs prism-e2e-lsb; docker logs prism-e2e-proxy; } 2>&1)
for s in "${SECRETS[@]}"; do
	echo "$alllogs" | grep -qF "$s" && leak=1
	echo "$alllogs" | grep -qF "${s##*_}" && leak=1
done
check "no token secret in any container log" test $leak = 0
proxy_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' prism-e2e-proxy)
check "proxied requests are audited with the forwarded client address, not the proxy's ($proxy_ip)" \
	bash -c "echo '$audit' | grep '\"tokenName\":\"ci-app\"' | grep -vq '\"client\":\"$proxy_ip\"'"

# ---------------------------------------------------------------- metrics
log "metrics"
ma=$(curl -s "$LSA_OPS/metrics"); mb=$(curl -s "$LSB_OPS/metrics")
sum() { echo "$1$2" | awk -v pat="$3" 'index($0, pat) == 1 {s += $NF} END {print s + 0}'; }
up200=$(sum "$ma" $'\n'"$mb" 'prism_link_requests_total{endpoint="upload",code="200"}')
up403=$(sum "$ma" $'\n'"$mb" 'prism_link_requests_total{endpoint="upload",code="403"}')
authf=$(sum "$ma" $'\n'"$mb" 'prism_link_auth_failures_total')
echo "  uploads 200=$up200 403=$up403 auth failures=$authf"
check "/metrics on both ops ports counts the 7 accepted uploads" test "$up200" -ge 7
check "/metrics counts the 403 upload" test "$up403" -ge 1
check "/metrics counts auth failures" test "$authf" -ge 1
check "both replicas report the same repositories gauge" bash -c "[ \"\$(echo '$ma' | grep '^prism_link_repositories ')\" = \"\$(echo '$mb' | grep '^prism_link_repositories ')\" ]"
code=$(curl -s -o /dev/null -w '%{http_code}' "$LSA/metrics")
check "/metrics is not on the public port without the ops listener" bash -c "[ '$code' != 200 ]"

# ---------------------------------------------------------------- rate limit and mTLS
log "rate limit (--rate 2) and mTLS on a short-lived replica"
docker run -d --name prism-e2e-rate --read-only --cap-drop ALL --security-opt no-new-privileges:true \
	--tmpfs /data:uid=65532,gid=65532,mode=0700 \
	-v "$E2E_DIR/tokens:/etc/prism:ro" -v "$C:/certs:ro" \
	-p 127.0.0.1:18893:8890 -p 127.0.0.1:19893:9890 "$IMAGE" \
	link-server --dir /data --tokens /etc/prism/tokens.json --rate 2 --ops-addr :9890 \
	--tls-cert /certs/server.crt --tls-key /certs/server.key --client-ca /certs/ca.crt >/dev/null
check "rate replica healthy" wait_http "$RATE_OPS/health"
MTLS=(--cacert "$C/ca.crt" --cert "$C/client.crt" --key "$C/client.key")
code=$(curl -s -o /dev/null -w '%{http_code}' --cacert "$C/ca.crt" -H "Authorization: Bearer $ADMIN" "$RATE/link/repos")
check "mTLS: no client certificate is refused" test "$code" = 000
out=$(push "$R/app" "$APP" "$RATE" "$CI" PRISM_LINK_CLIENT_CERT="$C/client.crt" PRISM_LINK_CLIENT_KEY="$C/client.key")
if echo "$out" | grep -q "^pushed $APP@"; then pass "mTLS: push with PRISM_LINK_CLIENT_CERT/KEY"; else fail "mTLS: push with PRISM_LINK_CLIENT_CERT/KEY" "$out"; fi
sleep 2.5 # refill the bucket
codes=$(for _ in $(seq 1 20); do curl -s -o /dev/null -w '%{http_code}\n' "${MTLS[@]}" -H "Authorization: Bearer $ADMIN" "$RATE/link/repos"; done)
n200=$(echo "$codes" | grep -c '^200$'); n429=$(echo "$codes" | grep -c '^429$')
echo "  20 rapid requests: 200=$n200 429=$n429"
check "rate limit: 429 when hammered (burst 4)" bash -c "[ $n429 -ge 10 ] && [ $n200 -ge 1 ] && [ $n200 -le 6 ]"
ra=$(curl -s -D - -o /dev/null "${MTLS[@]}" -H "Authorization: Bearer $ADMIN" "$RATE/link/repos" | tr -d '\r' | awk -F': ' 'tolower($1) == "retry-after" {print $2}')
check "429 carries Retry-After ($ra)" test -n "$ra"
code=$(curl -s -o /dev/null -w '%{http_code}' "${MTLS[@]}" -H "Authorization: Bearer $READER" "$RATE/link/repos")
check "rate limit is per token (another token still 200)" test "$code" = 200
rl=$(curl -s "$RATE_OPS/metrics" | awk '/^prism_link_rate_limited_total/ {s += $NF} END {print s + 0}')
check "/metrics counts rate-limited requests ($rl)" test "$rl" -ge 10

# ---------------------------------------------------------------- summary
echo
echo "================ summary"
printf '%s\n' "${RESULTS[@]}"
echo "passed $PASSES, failed $FAILS, known bugs $KNOWN"
[ $FAILS = 0 ]
