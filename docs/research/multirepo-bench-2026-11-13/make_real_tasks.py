#!/usr/bin/env python3
"""Writes real_tasks.json: real cross-repository changes (library commit + the consumer
commit that needed it), all after June 2026. Statements are the consumer PR's problem
text (or its linked issue), plus the public names the gold tests call, never the fix."""
import json
import subprocess
from pathlib import Path

CORPUS = Path("~/.cache/prism-research/multirepo-corpus").expanduser()


def r(repo, rev):
    return subprocess.run(["git", "-C", str(CORPUS / repo), "rev-parse", rev], capture_output=True, text=True,
                          check=True).stdout.strip()


def t(id, lib, app, lib_repo, app_repo, lib_companion, lib_gold, app_commit, stmt, lib_base=None, **kw):
    d = dict(id=id, lib=lib, app=app, lib_repo=lib_repo, app_repo=app_repo,
             lib_base=r(lib_repo, lib_base or lib_companion + "^"), lib_gold=r(lib_repo, lib_gold),
             lib_test_commits=[r(lib_repo, lib_companion)],
             app_base=r(app_repo, app_commit + "^"), app_gold=r(app_repo, app_commit),
             app_test_commits=[r(app_repo, app_commit)], statement=stmt)
    d.update(kw)
    return d


K, B, G, P = "ipfs_kubo", "ipfs_boxo", "provasign_grove", "provasign_prism"
kubo = dict(build_bin="cmd/ipfs/ipfs", build_pkg="./cmd/ipfs")
tasks = [
    t("kubo-bootstrap-empty-list", "boxo", "kubo", B, K, "2edf737d", "2edf737d", "372b1e3ae",
      "Routing.Type=none still runs bootstrap process, dials stale backup peers and triggers external DNS "
      "resolution.\n\nWhen the node was previously run with default routing, bootstrap peers are persisted to the "
      "datastore's backup bootstrap peer list (TempBootstrapPeersKey = /local/temp_bootstrap_peers). Later, with an "
      "empty `Bootstrap` list (for example `Routing.Type=none` and `Bootstrap=null`), the daemon still dials those "
      "stale backup peers every ~30s (DefaultBootstrapConfig.Period), triggering external DNS resolution and failed "
      "connection attempts. An empty Bootstrap list should disable all bootstrap dialing, including the saved backup "
      "peers.", **kubo),
    t("kubo-ipfs-uri-header", "boxo", "kubo", B, K, "63cae36a", "63cae36a", "873fab733",
      "Legacy `X-Ipfs-Path` cannot carry every UnixFS file name: bytes outside visible ASCII are invalid in HTTP "
      "field values (RFC 9110 5.5), so names like `łódź.txt` reach clients garbled. ipfs/specs#548 (IPIP-548) "
      "deprecates it in favor of `Ipfs-Uri`, a percent-encoded `ipfs://` / `ipns://` address that survives any "
      "name.\n\nGateway responses should carry the `Ipfs-Uri` header and no longer send `X-Ipfs-Path`. Add an "
      "opt-in `Gateway.DeprecatedXIpfsPath` config flag that restores the legacy header to ease migration; even then "
      "it is skipped when the value would include non-ASCII byte sequences.", **kubo),
    t("kubo-native-ipfs-uris", "boxo", "kubo", B, K, "8a6d1d21", "8a6d1d21", "4c227f07e",
      "Commands should accept native IPFS URIs like `ipfs://cid` and `ipns://name` (and the schemeless `ipfs:cid` "
      "/ `ipns:name` forms), so a link can be pasted straight from a browser or another tool instead of being "
      "rewritten into an `/ipfs/` path first. This must stay backward-compatible: `/ipfs/cid` paths and bare CIDs "
      "keep working, and a URI is one more accepted form. The URI parsing belongs in boxo's `path` package as "
      "`NewPathFromURI`, next to the existing path constructors.", **kubo),
    t("kubo-ipns-lifetime-bounds", "boxo", "kubo", B, K, "38baa62f", "c16c2378", "dcaa85014",
      "IPNS responses could be reused past a record's validity, and `ipfs name publish` accepted self-defeating "
      "settings. A cache (CDN, browser, Helia) could serve a record whose signature had already expired, which "
      "validating clients reject as sporadic 500s; `ipfs name publish` took a non-positive `--lifetime`, a negative "
      "`--ttl`, or a `--ttl` longer than `--lifetime`; and a republisher with `Ipns.RecordLifetime` shorter than "
      "`Ipns.RepublishPeriod` let records expire before the next cycle, leaving names unresolvable.", **kubo),
    t("prism-quickstatus", "grove", "prism", G, P, "fc39867b", "v0.21.1", "d8bc724",
      "`prism status` takes 1.3 s on a large index (500k edges) because it asks Grove for a full status, which "
      "loads the graph. Status only needs what the store already records. Add a store-only status to Grove's public "
      "API (`pkg/grove`, `QuickStatus`) that answers without building the graph, and use it for `prism status`."),
    t("prism-doctor-capabilities", "grove", "prism", G, P, "c65ce2db", "v0.58.3", "da8e451",
      "`prism doctor` cannot say which languages get compiler-backed analysis and which fall back to name matching: "
      "Grove computes a per-language capability manifest for `grove doctor`, but it is not exported to embedders. "
      "Export the capability manifest from Grove's public package (`pkg/grove`) and have `prism doctor` surface it "
      "per language."),
    t("prism-heuristic-refs-field", "grove", "prism", G, P, "b6356849", "v0.38.1", "ca551b1",
      "Grove reports that a change-impact result includes heuristic (name-matched) references by appending a suffix "
      "to the `Completeness` string. Consumers that compare Completeness values (\"closed\", \"type-level\", ...) "
      "break on the suffixed value, and prism cannot reliably tell agents when heuristic references are present. "
      "Report it as its own boolean field on the change-impact result instead (`HasHeuristicRefs`), keep "
      "Completeness to its plain values, and carry the field through prism's change-impact answer."),
    t("clientgolang-openmetrics2-rc", "common", "client_golang", "prometheus_common", "prometheus_client_golang",
      "220c51a", "v0.73.0", "3538594",
      "promhttp should support OpenMetrics 2.0-rc: when `EnableOpenMetrics` is set and `AcceptedFormats` is unset, "
      "the handler should offer the OpenMetrics 2.0.0-rc format ahead of the stable OpenMetrics formats. expfmt in "
      "prometheus/common has no format constant for it yet; add `FmtOpenMetrics_2_0_0_rc` there next to the other "
      "OpenMetrics formats."),
]
out = Path(__file__).resolve().parent / "real_tasks.json"
out.write_text(json.dumps(tasks, indent=1, ensure_ascii=False))
print(len(tasks), "tasks ->", out)
