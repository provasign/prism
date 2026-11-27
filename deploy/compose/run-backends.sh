#!/usr/bin/env bash
# Every storage backend through the container image: for each emulator, two
# link-server replicas on one store, checked from the host with a host-built
# prism. TLS, mTLS and rate limits do not depend on the backend; run-e2e.sh
# checks them (on S3).
#
#   deploy/compose/run-backends.sh                 # all backends
#   BACKENDS="azure gcs" deploy/compose/run-backends.sh
#   PRISM_BIN=/path/prism IMAGE=prism:tag SKIP_BUILD=1 deploy/compose/run-backends.sh
#
# Backends: s3-versitygw s3-seaweedfs s3-localstack azure gcs. Needs Docker,
# curl, git, Go. Containers are named prism-e2e-* and removed at the end.
set -uo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
BACKENDS=${BACKENDS:-"s3-versitygw s3-seaweedfs s3-localstack azure gcs"}
IMAGE=${IMAGE:-prism-e2e-image:backends}
NET=prism-e2e-net
WORK=$(mktemp -d)
P=${PRISM_BIN:-$WORK/prism}
AZ_KEY='Eby8vdM02xNOcqFlqUwJPLlmEtlCDXJ1OUzFT50uSRZ6IFsuFq2UVErCz4I6tq/K1SZFPTOtr/KBHBeksoGMGw=='
PASSES=0 FAILS=0
declare -a RESULTS=()

pass() { PASSES=$((PASSES + 1)); RESULTS+=("PASS  $1"); echo "PASS  $1"; }
fail() { FAILS=$((FAILS + 1)); RESULTS+=("FAIL  $1 -- $2"); echo "FAIL  $1 -- $2"; }
check() { local name=$1; shift; if "$@" >/dev/null 2>&1; then pass "$name"; else fail "$name" "command failed: $*"; fi; }

cleanup() {
	docker rm -f prism-e2e-emu prism-e2e-lsa prism-e2e-lsb >/dev/null 2>&1
	docker network rm $NET >/dev/null 2>&1
	rm -rf "$WORK"
}
trap cleanup EXIT

if [ "${SKIP_BUILD:-0}" != 1 ]; then
	(cd "$ROOT" && GOWORK=off go build -o "$WORK/prism" ./cmd/prism) || { echo "host build failed"; exit 2; }
	P=$WORK/prism
	docker build -q -f "$ROOT/deploy/docker/Dockerfile" --build-arg VERSION=backends-e2e -t "$IMAGE" "$ROOT" >/dev/null || { echo "image build failed"; exit 2; }
fi
docker network create $NET >/dev/null 2>&1

g() { git -c user.name=t -c user.email=t@example.com "$@"; }

# The repositories: a library and a consumer that calls it.
mkrepos() {
	local d=$WORK/repos/$1
	rm -rf "$d"; mkdir -p "$d/lib" "$d/app"
	printf 'module example.com/acme/lib\n\ngo 1.22\n' >"$d/lib/go.mod"
	printf 'package lib\n\n// Normalize trims a key.\nfunc Normalize(s string) string { return s }\n' >"$d/lib/lib.go"
	printf 'module example.com/acme/app\n\ngo 1.22\n\nrequire example.com/acme/lib v0.1.0\n\nreplace example.com/acme/lib => ../lib\n' >"$d/app/go.mod"
	printf 'package main\n\nimport "example.com/acme/lib"\n\nfunc main() { println(lib.Normalize("A")) }\n' >"$d/app/main.go"
	for r in lib app; do (cd "$d/$r" && g init -q -b main && g add -A && g commit -q -m init); done
	echo "$d"
}

wait_health() { for _ in $(seq 1 100); do curl -sf "$1/health" >/dev/null && return 0; sleep 0.2; done; return 1; }

run_backend() {
	local b=$1 emu_port flags=() env=() hostflags=() hostenv=()
	echo "==== $b"
	docker rm -f prism-e2e-emu prism-e2e-lsa prism-e2e-lsb >/dev/null 2>&1
	case $b in
	s3-versitygw)
		docker run -d --name prism-e2e-emu --network $NET -p 127.0.0.1:17401:7070 -e ROOT_ACCESS_KEY=e2ekey -e ROOT_SECRET_KEY=e2esecret-e2esecret \
			versity/versitygw:latest --port :7070 posix /tmp >/dev/null
		emu_port=17401; flags=(--s3 s3://prism-e2e/link --s3-endpoint http://prism-e2e-emu:7070 --s3-path-style)
		hostflags=(--s3 s3://prism-e2e/link --s3-endpoint http://127.0.0.1:$emu_port --s3-path-style)
		env=(-e AWS_ACCESS_KEY_ID=e2ekey -e AWS_SECRET_ACCESS_KEY=e2esecret-e2esecret); hostenv=(AWS_ACCESS_KEY_ID=e2ekey AWS_SECRET_ACCESS_KEY=e2esecret-e2esecret)
		;;
	s3-seaweedfs)
		printf '{"identities":[{"name":"e2e","credentials":[{"accessKey":"e2ekey","secretKey":"e2esecret-e2esecret"}],"actions":["Admin","Read","Write","List","Tagging"]}]}' >"$WORK/s3.json"
		docker run -d --name prism-e2e-emu --network $NET -p 127.0.0.1:17402:8333 -v "$WORK/s3.json:/etc/s3.json:ro" \
			chrislusf/seaweedfs:latest server -s3 -s3.config=/etc/s3.json >/dev/null
		emu_port=17402; flags=(--s3 s3://prism-e2e/link --s3-endpoint http://prism-e2e-emu:8333 --s3-path-style)
		hostflags=(--s3 s3://prism-e2e/link --s3-endpoint http://127.0.0.1:$emu_port --s3-path-style)
		env=(-e AWS_ACCESS_KEY_ID=e2ekey -e AWS_SECRET_ACCESS_KEY=e2esecret-e2esecret); hostenv=(AWS_ACCESS_KEY_ID=e2ekey AWS_SECRET_ACCESS_KEY=e2esecret-e2esecret)
		;;
	s3-localstack)
		# LocalStack 3.8 (the last community image) accepts If-Match and
		# ignores it: the server must refuse to start on it.
		docker run -d --name prism-e2e-emu --network $NET -p 127.0.0.1:17403:4566 -e SERVICES=s3 localstack/localstack:3.8 >/dev/null
		emu_port=17403; flags=(--s3 s3://prism-e2e/link --s3-endpoint http://prism-e2e-emu:4566 --s3-path-style)
		hostflags=(--s3 s3://prism-e2e/link --s3-endpoint http://127.0.0.1:$emu_port --s3-path-style)
		env=(-e AWS_ACCESS_KEY_ID=test -e AWS_SECRET_ACCESS_KEY=test); hostenv=(AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test)
		;;
	azure)
		docker run -d --name prism-e2e-emu --network $NET -p 127.0.0.1:17404:10000 mcr.microsoft.com/azure-storage/azurite \
			azurite-blob --blobHost 0.0.0.0 --skipApiVersionCheck >/dev/null
		emu_port=17404; flags=(--azure http://prism-e2e-emu:10000/devstoreaccount1/prism-e2e/link --azure-path-style)
		hostflags=(--azure http://127.0.0.1:$emu_port/devstoreaccount1/prism-e2e/link --azure-path-style)
		env=(-e "AZURE_STORAGE_KEY=$AZ_KEY"); hostenv=("AZURE_STORAGE_KEY=$AZ_KEY")
		;;
	gcs)
		docker run -d --name prism-e2e-emu --network $NET -p 127.0.0.1:17405:4443 fsouza/fake-gcs-server -scheme http -port 4443 >/dev/null
		emu_port=17405; flags=(--gcs gs://prism-e2e/link)
		hostflags=(--gcs gs://prism-e2e/link)
		env=(-e STORAGE_EMULATOR_HOST=prism-e2e-emu:4443); hostenv=(STORAGE_EMULATOR_HOST=127.0.0.1:$emu_port)
		;;
	esac
	# The emulator up, and the bucket or container created.
	local up=0
	for _ in $(seq 1 150); do
		case $b in
		s3-*) curl -s -o /dev/null -X PUT --aws-sigv4 "aws:amz:us-east-1:s3" --user "$(echo "${hostenv[0]}" | cut -d= -f2):$(echo "${hostenv[1]}" | cut -d= -f2)" \
			"http://127.0.0.1:$emu_port/prism-e2e" -w '%{http_code}' 2>/dev/null | grep -qE '200|409' && up=1 ;;
		azure) (cd "$ROOT" && env "${hostenv[@]}" PRISM_TEST_AZURE=http://127.0.0.1:$emu_port/devstoreaccount1/prism-e2e PRISM_TEST_AZURE_PATH_STYLE=1 \
			GOWORK=off go test -count=1 -run TestAzureBlobAgainstARealServer ./internal/linkserver/ >/dev/null 2>&1) && up=1 ;;
		gcs) curl -sf -X POST -H 'Content-Type: application/json' -d '{"name":"prism-e2e"}' "http://127.0.0.1:$emu_port/storage/v1/b" >/dev/null 2>&1 && up=1 ;;
		esac
		[ $up = 1 ] && break
		sleep 1
	done
	if [ $up != 1 ]; then fail "$b: storage emulator ready" "no bucket after 150 s"; return; fi
	pass "$b: storage emulator ready, bucket created"
	if [ "$b" = s3-localstack ]; then
		out=$(docker run --rm --network $NET "${env[@]}" "$IMAGE" link-server "${flags[@]}" --no-auth 2>&1)
		if echo "$out" | grep -q 'ignores conditional writes'; then pass "$b: the server refuses a store that ignores conditional writes"; else
			fail "$b: refuses a store without conditional writes" "$out"; fi
		docker run -d --name prism-e2e-lsa --network $NET "${env[@]}" "$IMAGE" link-server "${flags[@]}" --no-auth --single-replica >/dev/null
		sleep 4
		out=$(docker logs prism-e2e-lsa 2>&1)
		docker rm -f prism-e2e-lsa >/dev/null 2>&1
		if echo "$out" | grep -q 'safe only as the single replica'; then pass "$b: --single-replica starts on it, with a warning"; else
			fail "$b: --single-replica" "$out"; fi
		docker rm -f prism-e2e-emu >/dev/null 2>&1
		return
	fi

	local T=$WORK/$b-tokens; rm -rf "$T"; mkdir -p "$T"; chmod 755 "$T"
	local CI READER OUTSIDER TEMP ADMIN
	CI=$("$P" link-server token create --tokens "$T/tokens.json" --name ci-app --upload example.com/acme/app 2>/dev/null)
	READER=$("$P" link-server token create --tokens "$T/tokens.json" --name reader --read 'example.com/acme/*' 2>/dev/null)
	OUTSIDER=$("$P" link-server token create --tokens "$T/tokens.json" --name outsider --read 'other.org/*' 2>/dev/null)
	TEMP=$("$P" link-server token create --tokens "$T/tokens.json" --name temp --read '*' 2>"$WORK/temp.err")
	ADMIN=$("$P" link-server token create --tokens "$T/tokens.json" --name ops --admin --read '*' 2>/dev/null)
	local TEMP_ID; TEMP_ID=$(sed -n 's/^created token \([0-9a-f]*\) .*/\1/p' "$WORK/temp.err")
	chmod 644 "$T/tokens.json" # the container's non-root user reads the mounted copy

	local i
	for i in a b; do
		local port=$([ $i = a ] && echo 18901 || echo 18902) ops=$([ $i = a ] && echo 19901 || echo 19902)
		docker run -d --name prism-e2e-ls$i --network $NET -p 127.0.0.1:$port:8890 -p 127.0.0.1:$ops:9890 \
			-v "$T:/etc/prism/tokens:ro" "${env[@]}" "$IMAGE" link-server "${flags[@]}" \
			--tokens /etc/prism/tokens/tokens.json --refresh 2s --keep 2 --ops-addr :9890 >/dev/null
	done
	local A=http://127.0.0.1:18901 B=http://127.0.0.1:18902
	if wait_health $A && wait_health $B; then pass "$b: both replicas up"; else
		fail "$b: both replicas up" "$(docker logs prism-e2e-lsa 2>&1 | tail -3)"; return
	fi

	local d; d=$(mkrepos "$b")
	(cd "$d/app" && "$P" index . >/dev/null 2>&1)
	check "$b: CI token uploads its repository through replica A" env PRISM_LINK_TOKEN="$CI" bash -c "cd '$d/app' && '$P' workspace push --server $A --repo example.com/acme/app --ref main"
	out=$(cd "$d/app" && PRISM_LINK_TOKEN="$CI" "$P" workspace push --server $A --repo example.com/acme/other --ref main 2>&1)
	if echo "$out" | grep -q '403 Forbidden'; then
		pass "$b: CI token may not upload another repository (403)"; else fail "$b: CI token may not upload another repository (403)" "$out"; fi

	printf 'workspace_server: %s\n' $B >"$d/lib/prism.yaml"
	(cd "$d/lib" && "$P" index . >/dev/null 2>&1)
	local out seen=0
	for _ in $(seq 1 30); do
		out=$(cd "$d/lib" && PRISM_LINK_TOKEN="$READER" "$P" change-impact Normalize --format text 2>&1)
		echo "$out" | grep -q 'example.com/acme/app:main.go:5' && { seen=1; break; }
		sleep 0.3
	done
	if [ $seen = 1 ]; then pass "$b: replica B answers the upload through A (reader's change-impact)"; else fail "$b: replica B answers the upload through A" "$(echo "$out" | grep workspaceScope)"; fi
	out=$(cd "$d/lib" && PRISM_LINK_TOKEN="$OUTSIDER" "$P" change-impact Normalize --format text 2>&1)
	if echo "$out" | grep -q 'NOT READABLE' && ! echo "$out" | grep -q 'acme/app:main.go'; then pass "$b: a token without read rights gets NOT READABLE, no sites"; else
		fail "$b: unreadable" "$(echo "$out" | grep workspaceScope)"; fi

	local code
	code=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TEMP" -X POST -d '{"keys":[]}' $B/link/consumers)
	[ "$code" = 200 ] || fail "$b: temp token works before revocation" "$code"
	"$P" link-server token revoke --tokens "$T/tokens.json" "$TEMP_ID" >/dev/null
	chmod 644 "$T/tokens.json"
	local revoked=0
	for _ in $(seq 1 40); do
		ca=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TEMP" -X POST -d '{"keys":[]}' $A/link/consumers)
		cb=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TEMP" -X POST -d '{"keys":[]}' $B/link/consumers)
		[ "$ca" = 401 ] && [ "$cb" = 401 ] && { revoked=1; break; }
		sleep 0.25
	done
	if [ $revoked = 1 ]; then pass "$b: a revoked token is refused by both replicas within 10 s"; else fail "$b: revocation" "A $ca B $cb"; fi

	# Retention: four more commits through replica B with --keep 2.
	for n in 2 3 4 5; do
		(cd "$d/app" && printf 'package main\n\nvar v%d = %d\n' $n $n >"v$n.go" && g add -A && g commit -q -m "c$n" && "$P" index . >/dev/null 2>&1 &&
			PRISM_LINK_TOKEN="$CI" "$P" workspace push --server $B --repo example.com/acme/app --ref main >/dev/null 2>&1)
	done
	out=$(env "${hostenv[@]}" "$P" link-server check "${hostflags[@]}" 2>&1)
	if echo "$out" | grep -q '^1 repositories, 2 bundles, 0 problems'; then pass "$b: retention keeps 2 commits; check reports 0 problems"; else fail "$b: retention and check" "$out"; fi
	# The uploads went through B; A sees them after its next refresh (2 s).
	head=$(cd "$d/app" && git rev-parse HEAD)
	local newest=0
	for _ in $(seq 1 30); do
		out=$(curl -s -H "Authorization: Bearer $ADMIN" $A/link/repos)
		echo "$out" | grep -q "\"main\":\"$head\"" && { newest=1; break; }
		sleep 0.25
	done
	if [ $newest = 1 ]; then pass "$b: replica A sees B's newest commit on main within its refresh (admin GET /link/repos)"; else fail "$b: main is the newest commit" "$out"; fi

	if docker logs prism-e2e-lsa 2>/dev/null | grep '"endpoint":"upload"' | grep -q '"tokenName":"ci-app"' &&
		! docker logs prism-e2e-lsa 2>&1 | grep -q 'plk_' && ! docker logs prism-e2e-lsb 2>&1 | grep -q 'plk_'; then
		pass "$b: audit lines name the token; no secret in either replica's log"; else fail "$b: audit" "missing lines or a secret logged"; fi
	if curl -s http://127.0.0.1:19902/metrics | grep -q 'prism_link_uploads_total{result="ok"} [1-9]'; then pass "$b: replica B's metrics count its uploads"; else fail "$b: metrics" "no uploads counted"; fi

	# Backup out of this backend, restore into a directory, check it.
	rm -rf "$WORK/$b-backup" "$WORK/$b-restored"
	if env "${hostenv[@]}" "$P" link-server backup "${hostflags[@]}" --to "$WORK/$b-backup" >/dev/null 2>&1 &&
		"$P" link-server restore --dir "$WORK/$b-restored" --from "$WORK/$b-backup" >/dev/null 2>&1 &&
		"$P" link-server check --dir "$WORK/$b-restored" | grep -q '^1 repositories, 2 bundles, 0 problems'; then
		pass "$b: backup to a directory and restore; the copy checks clean"; else fail "$b: backup and restore" "see $WORK"; fi
	docker rm -f prism-e2e-emu prism-e2e-lsa prism-e2e-lsb >/dev/null 2>&1
}

for b in $BACKENDS; do run_backend "$b"; done
echo
printf '%s\n' "${RESULTS[@]}"
echo "passed $PASSES, failed $FAILS"
[ $FAILS = 0 ]
