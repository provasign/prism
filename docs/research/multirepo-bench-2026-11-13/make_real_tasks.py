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
    # lib_companion: one commit, or a range "old..new" whose commits all carry library tests
    if ".." in lib_companion:
        old, new = lib_companion.split("..")
        lib_base = lib_base or old
        commits = subprocess.run(["git", "-C", str(CORPUS / lib_repo), "rev-list", "--reverse", "--no-merges",
                                  lib_companion], capture_output=True, text=True, check=True).stdout.split()
    else:
        lib_base = lib_base or lib_companion + "^"
        commits = [r(lib_repo, lib_companion)]
    d = dict(id=id, lib=lib, app=app, lib_repo=lib_repo, app_repo=app_repo,
             lib_base=r(lib_repo, lib_base), lib_gold=r(lib_repo, lib_gold),
             lib_test_commits=commits,
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
      "it is skipped when the value would include non-ASCII byte sequences.\n\nNames the tests use in boxo's "
      "`gateway` package: `func ipfsUriHeaderValue(contentPath path.Path) (string, bool)`, "
      "`func encodeIpfsUriSegment(segment string) string` (percent-encodes one path segment), "
      "`func isFieldValueSafe(s string) bool` (true when s is a valid HTTP field value of visible ASCII).", **kubo),
    t("kubo-native-ipfs-uris", "boxo", "kubo", B, K, "8a6d1d21", "8a6d1d21", "4c227f07e",
      "Commands should accept native IPFS URIs like `ipfs://cid` and `ipns://name` (and the schemeless `ipfs:cid` "
      "/ `ipns:name` forms), so a link can be pasted straight from a browser or another tool instead of being "
      "rewritten into an `/ipfs/` path first. This must stay backward-compatible: `/ipfs/cid` paths and bare CIDs "
      "keep working, and a URI is one more accepted form. The URI parsing belongs in boxo's `path` package as "
      "`NewPathFromURI`, next to the existing path constructors.\n\nNames the tests use: in boxo's `path` package "
      "`func NewPathFromURI(str string) (Path, error)` and `func normalizeURIScheme(str string) string` (rewrites an "
      "`ipfs:`, `ipns:` or `ipld:` URI, with or without `//`, scheme matched case-insensitively, to its `/ipfs/`, "
      "`/ipns/` or `/ipld/` path form; other input unchanged); in "
      "kubo's `core/commands/cmdutils` package `func CidFromArg(arg string) (cid.Cid, error)` (the root CID of a bare "
      "CID, an `/ipfs/` path or an IPFS URI; an argument pointing below the root, or a mutable path, is an error) next to the existing `PathOrCidPath`.", **kubo),
    t("kubo-ipns-lifetime-bounds", "boxo", "kubo", B, K, "38baa62f", "c16c2378", "dcaa85014",
      "IPNS responses could be reused past a record's validity, and `ipfs name publish` accepted self-defeating "
      "settings. A cache (CDN, browser, Helia) could serve a record whose signature had already expired, which "
      "validating clients reject as sporadic 500s; `ipfs name publish` took a non-positive `--lifetime`, a negative "
      "`--ttl`, or a `--ttl` longer than `--lifetime`; and a republisher with `Ipns.RecordLifetime` shorter than "
      "`Ipns.RepublishPeriod` let records expire before the next cycle, leaving names unresolvable.\n\nName the tests use "
      "in boxo's `gateway` package: `func ipnsRecordMaxAge(record *ipns.Record) (maxAge time.Duration, ok bool)` "
      "(how long a response carrying this record may be cached).", **kubo),
    t("prism-quickstatus", "grove", "prism", G, P, "fc39867b", "v0.21.1", "d8bc724",
      "`prism status` takes 1.3 s on a large index (500k edges) because it asks Grove for a full status, which "
      "loads the graph. Status only needs what the store already records. Add a store-only status to Grove's public "
      "API (`pkg/grove`, `QuickStatus`) that answers without building the graph, and use it for `prism status`.\n\n"
      "Signatures: grove `func QuickStatus(ctx context.Context, repoRoot string) (Status, error)`; prism's grove "
      "client `func (c *Client) QuickStatus(ctx context.Context) (*StatusResult, error)`."),
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
F, C = "charmbracelet_fantasy", "charmbracelet_crush"
tasks += [
    t("crush-gzip-requests", "fantasy", "crush", F, C, "8f57f94", "8f57f94", "1b37350c",
      "Every Crush turn re-sends the whole conversation to Hyper. Hyper now inflates gzip request bodies, so "
      "requests to the Hyper provider should be sent gzip-compressed (`Content-Encoding: gzip`), about four times "
      "smaller. Other openai-compatible providers must keep sending plain bodies, because nothing tells us they can "
      "read compressed ones. Fantasy's openai-compatible provider has no way to compress request bodies: add an "
      "opt-in `WithRequestCompression()` option (in `providers/openaicompat`, backed by `providers/openai`) and use "
      "it for Hyper. Small bodies may go uncompressed, and a request whose compressed body the server rejects should "
      "be retried uncompressed.\n\nNames the tests use in fantasy's `providers/openai` package: "
      "`func withRequestCompression(client option.HTTPClient) option.HTTPClient`, "
      "`func compressedBodyCandidate(req *http.Request) ([]byte, bool)`, "
      "`func gzipForWire(body []byte) ([]byte, bool)`, "
      "`func requestWithBody(req *http.Request, body []byte, gzipped bool) *http.Request`."),
    t("crush-brotli-requests", "fantasy", "crush", F, C, "a66d7f8", "a66d7f8", "1b7582db",
      "Crush gzip-compresses the requests it sends to Hyper. Brotli beats that gzip level on both size and CPU, so "
      "Hyper requests should prefer brotli (`Content-Encoding: br`), falling back to gzip and then to a plain body "
      "against an older Hyper that refuses brotli. Fantasy's `WithRequestCompression()` only knows gzip: let it take "
      "an ordered preference list of encodings (`RequestEncoding`, with `EncodingGzip` and `EncodingBrotli`, exported "
      "from both `providers/openai` and `providers/openaicompat`), and use brotli then gzip for Hyper. Use "
      "github.com/molecule-man/go-brrr for brotli.\n\nNames the tests use in fantasy's `providers/openai` package: "
      "`func withRequestCompression(client option.HTTPClient, compressor *requestCompressor) option.HTTPClient`, "
      "`func newRequestCompressor(preferences []RequestEncoding) *requestCompressor`, "
      "`func brotliForWire(body []byte) ([]byte, bool)`, `func gzipForWire(body []byte) ([]byte, bool)`, "
      "`func requestWithBody(req *http.Request, body []byte, encoding string) *http.Request` (encoding is the "
      "Content-Encoding value, empty for a plain body)."),
    t("crush-stream-reset", "fantasy", "crush", F, C, "f8664d5", "f8664d5", "8db57337",
      "When a provider drops an HTTP/2 stream mid-response (for example `stream error: stream ID 3; "
      "INTERNAL_ERROR; received from peer`), Crush ends the turn with the raw Go transport error. Such a request "
      "should be retried transparently instead. Partial content from the failed attempt must be cleared before each "
      "retry so the recovered response does not concatenate onto it, and a transport error that still surfaces "
      "should be shown as a concise message instead of internal Go error text.\n\nFantasy should classify these: "
      "`func IsTransportError(err error) bool` and `func NewTransportError(err error) *ProviderError` in package "
      "`fantasy`, transport errors retryable by `isRetryableError`, a `cleanHTTP2ErrorMessage(msg string) string` "
      "helper for the concise text, and the openai provider's stream must surface a reset as a transport error. "
      "In Crush, `(*message.Message).ResetStreamedContent()` clears streamed text, reasoning, tool calls and the "
      "finish part, keeping image and tool-result parts."),
]
tasks += [
    t("cosign-user-agent", "sigstore-go", "cosign", "sigstore_sigstore-go", "sigstore_cosign", "0161b20", "0161b20",
      "f8e6ea94",
      "Cosign should send an HTTP User-Agent that names cosign and its version on every request (TUF, Fulcio, "
      "Rekor, TSA, Dex). Today most requests identify only as `sigstore-go/$VERSION`; ideally they read "
      "`cosign/$VERSION sigstore-go/$OTHER_VERSION`. sigstore-go gives callers no way to set or extend the "
      "User-Agent of the Fulcio, timestamp-authority and Rekor clients it builds for signing (`pkg/sign`). Let "
      "callers pass one (a `UserAgent` string on `FulcioOptions`, `TimestampAuthorityOptions` and `RekorOptions`), "
      "with sigstore-go still appending its own `sigstore-go/<version>`, and have cosign pass its own. Cosign's "
      "user agent lives in a new `internal/useragent` package: `Get() string` returns "
      "`cosign/<version> (<GOOS>; <GOARCH>)`.\n\nName the tests use in sigstore-go's `pkg/sign` package: "
      "`func userAgent(ua string) string` (the caller's agent with sigstore-go's appended; sigstore-go's alone "
      "when ua is empty)."),
    t("ice-turn-context", "turn", "ice", "pion_turn", "pion_ice", "48c1d3b", "48c1d3b", "e313f8c",
      "TURN candidate gathering in pion/ice cannot be interrupted: closing the agent while it gathers relay "
      "candidates blocks for about 7 seconds, waiting for the TURN allocation to time out (pion/ice#781). The TURN "
      "client in pion/turn offers no way to cancel an allocation or a STUN transaction. Add context-aware variants "
      "to `turn.Client`: `AllocateWithContext(ctx context.Context) (net.PacketConn, error)`, "
      "`AllocateTCPWithContext(ctx context.Context) (*client.TCPAllocation, error)` and "
      "`PerformTransactionWithContext(msg *stun.Message, to net.Addr, ignoreResult bool, ctx context.Context) "
      "(client.TransactionResult, error)`, keeping the existing methods; and have ice gather TURN candidates with "
      "the agent's context so Close returns promptly."),
    t("turn-unknown-attrs", "stun", "turn", "pion_stun", "pion_turn", "e1234ac", "e1234ac", "946de4e",
      "pion/stun encodes the UNKNOWN-ATTRIBUTES attribute with 4 bytes per attribute type instead of the 2 bytes "
      "RFC 5389 (section 15.9) specifies. On send, the peer sees extra zero attributes; on receive, half of the "
      "attributes are silently discarded when pion decodes the packet. Fix the encoding and decoding of "
      "`stun.UnknownAttributes`, and make pion/turn's server tests that inspect the UNKNOWN-ATTRIBUTES a server "
      "returns for an unknown comprehension-required attribute check the RFC length."),
    t("webtransport-capsule-api", "quic-go", "webtransport-go", "quic-go_quic-go", "quic-go_webtransport-go",
      "7d158866", "7d158866", "3ae01c0",
      "quic-go's http3 capsule API makes it hard for consumers to check that a capsule carried no trailing data "
      "after its expected content (quic-go/quic-go#5728). That was tolerable for CONNECT-IP's variable-length "
      "capsules, but in WebTransport most capsules are a single QUIC varint, so a malformed capsule with trailing "
      "bytes goes unnoticed. Add a stricter API to quic-go's `http3` package that makes it impossible to leave "
      "trailing data in a capsule: `NewCapsuleParser(r io.Reader) *CapsuleParser`, whose "
      "`Next() (CapsuleType, CapsuleReader, error)` returns the next capsule's type and a reader for it, only once "
      "the previous capsule was fully consumed; `CapsuleReader` has `Read`, `ReadByte`, `Remaining() int64` and "
      "`Discard() error`, and copies of a reader share progress. Port webtransport-go's capsule parsing to it, "
      "rejecting capsules with trailing data or oversize values.\n\nNames the tests use in webtransport-go: "
      "`func parseNextCapsule(parser *http3.CapsuleParser) (capsule, error)`, "
      "`parseCloseSessionCapsule(r http3.CapsuleReader)`, `parseMaxStreamsCapsule(r http3.CapsuleReader) (uint64, "
      "error)`, `parseStreamsBlockedCapsule(r http3.CapsuleReader) (uint64, error)`."),
]
TS, WG = "tailscale_tailscale", "tailscale_wireguard-go"
tasks += [
    t("ts-per-peer-psk", "wireguard-go", "tailscale", WG, TS, "e855235", "e855235", "31d8badb3",
      "Tailscale cannot give a WireGuard peer a pre-shared key. Peers are created lazily: wireguard-go asks a "
      "lookup function for a new peer's settings (`device.NewPeerConfig`), and tailscale's wgengine answers with "
      "the peer's allowed IPs only. Neither the lazily created peers nor the active peers tailscale synchronizes "
      "can carry a PSK, so the optional WireGuard pre-shared-key layer is unreachable.\n\nwireguard-go should let a "
      "caller set a PSK on a newly created peer (a `PresharedKey NoisePresharedKey` field on `device.NewPeerConfig`) "
      "and on an active one (`(*device.Peer).SetPresharedKey(psk NoisePresharedKey)`); the zero value disables the "
      "layer. In tailscale, the per-peer config source should return a `wgcfg.PeerConfig` (`AllowedIPs "
      "[]netip.Prefix`, `PresharedKey device.NoisePresharedKey`) instead of allowed IPs, carried through "
      "`wgcfg.NewPeerLookupFunc(bind conn.Bind, logf logger.Logf, peerConfig func(device.NoisePublicKey) "
      "(PeerConfig, bool)) device.PeerLookupFunc`, the engine's `SetPeerConfigFunc`, and LocalBackend; LocalBackend "
      "keeps returning no key for now."),
    t("webrtc-sctp-startcontext", "sctp", "webrtc", "pion_sctp", "pion_webrtc", "1d1ad5f", "1d1ad5f", "91bfc6c2",
      "pion/webrtc's transports gained context-aware start APIs, but `SCTPTransport` still only has "
      "`Start(capabilities SCTPCapabilities) error`, so SCTP association setup cannot be cancelled or bounded by a "
      "deadline. pion/sctp has no exported way to establish a client association under a context either. Export one "
      "from pion/sctp, `ClientContext(ctx context.Context, opts ...ClientOption) (*Association, error)`, where the "
      "context controls establishment only (cancelling it after the association is up has no effect), including "
      "the SNAP setup path; and add `(*SCTPTransport).StartContext(ctx context.Context, capabilities "
      "SCTPCapabilities) error` to pion/webrtc, with `Start` keeping its behavior."),
    t("cosign-x509-chain", "sigstore-go", "cosign", "sigstore_sigstore-go", "sigstore_cosign", "cbf26e4", "cbf26e4",
      "d6d86c22",
      "Cosign cannot sign or verify with self-managed (non-Fulcio) certificates that come with a certificate chain "
      "when using the Sigstore bundle format. sigstore-go refuses to parse a v0.3 bundle whose verification material "
      "is an `x509CertificateChain`, although sigstore maintainers agreed a chain is fine in a v0.3 bundle and that "
      "sigstore-go's constraint should be relaxed for callers that opt in.\n\nsigstore-go (`pkg/bundle`): add an opt-in "
      "`Option` type (`type Option func(*Bundle)`) with `AllowCertificateChain() Option`, accepted by "
      "`NewBundle(pbundle *protobundle.Bundle, opts ...Option) (*Bundle, error)` and "
      "`LoadJSONFromPath(path string, opts ...Option) (*Bundle, error)`; with it, a v0.3 bundle may carry a chain, "
      "whose intermediates the verification content exposes (`(*Certificate).Intermediates() []*x509.Certificate`) "
      "so they can join the trust store.\n\ncosign: when `cosign sign` gets a `--certificate-chain`, put the leaf and "
      "its chain (minus the self-signed root, leaf not duplicated) in the bundle; add `--allow-certificate-chain` to "
      "verification, carried as `CheckOpts.AllowCertificateChain` and `(*CheckOpts).BundleOptions() "
      "[]sgbundle.Option` (one option when allowed, none otherwise), and an `oci/remote` option "
      "`WithBundleOptions(opts ...sgbundle.Option) Option` stored in `options.BundleOpts`. The bundle signer's "
      "certificate provider for a local cert and chain is `localCertChainProvider{cert, chain []byte}` in "
      "`pkg/cosign/bundle`, implementing sigstore-go's `GetCertificateChain`."),
]
LK, LP, LM = "livekit_livekit", "livekit_protocol", "livekit_mediatransportutil"
tasks += [
    t("lk-webhook-drain", "protocol", "livekit", LP, LK, "0aaaf116", "0aaaf116", "63ad72da",
      "room_finished webhook not sent for rooms closed by a graceful shutdown.\n\nOn a graceful shutdown (SIGTERM, "
      "e.g. a rolling deploy), rooms still open on the node are closed with `ROOM_END_SERVER_SHUTDOWN`, but their "
      "`room_finished` webhooks are usually never sent. The process exits while the webhooks are still queued. This "
      "hits rooms that are open but empty when the node stops: a room whose last participant left within "
      "`departure_timeout`, or a room created ahead of its participants. The room's state is deleted as part of the "
      "shutdown, so no other node sends it later.\n\nCause: `LivekitServer.Start` returns right after "
      "`roomManager.Stop()`, `signalServer.Stop()` and `ioService.Stop()`, and `main` exits. `RoomManager.Stop` "
      "closes the rooms, which queues `RoomEnded` on the telemetry service's ops queue. Nothing flushes that queue "
      "or stops the webhook notifier before `Start` returns. In livekit/protocol, `ResourceURLNotifier.Stop(false)` "
      "asks its per-resource queues to drain but returns without waiting for them, unlike `URLNotifier.Stop(false)`, "
      "which waits for its queue to drain.\n\nThe telemetry service should get a `Stop()` method (on the "
      "`TelemetryService` interface and its generated fake) that sends what is queued, including the WEBHOOK "
      "analytics events for webhooks sent while the notifier drains, without letting an unreachable webhook "
      "endpoint hold up shutdown indefinitely."),
    t("lk-h264-profiles", "protocol", "livekit", LP, LK, "cdb9dafee4fe..28e604c0", "28e604c0", "436a0cc3",
      "Support more H.264 profiles in livekit-server. macOS needs the constrained-baseline level `42001f` to use its "
      "hardware encoder, so it should be offered by default (packetization modes 0 and 1). The H.264 main profile "
      "(`4d001f`) is not supported by default, for compatibility, but a user can enable it for a special scenario by "
      "setting its fmtp explicitly in the codec config; a codec entry that names only the mime type must not enable "
      "it. Payload types (including each codec's RTX payload type, PT+1) must stay unique.\n\nlivekit/protocol "
      "(`codecs` package) should define `H264ProfileLevelId42001fPacketizationMode0CodecParameters`, "
      "`H264ProfileLevelId42001fPacketizationMode1CodecParameters`, `H264ProfileLevelId4d001fPacketizationMode0CodecParameters` "
      "and `H264ProfileLevelId4d001fPacketizationMode1CodecParameters` (fmtp "
      "`level-asymmetry-allowed=1;packetization-mode=<0|1>;profile-level-id=<42001f|4d001f>`), with the `42001f` "
      "ones in the default video codec list. In livekit-server's `pkg/rtc`, the tests use "
      "`isCodecEnabledWithFmtp(codecs []*livekit.Codec, cap webrtc.RTPCodecCapability, strictFmtp bool) bool` "
      "(strict: only an exact fmtp match enables the codec) next to the existing `IsCodecEnabled`."),
    t("lk-h264-baseline-default", "protocol", "livekit", LP, LK, "e99ee7f1", "e99ee7f1", "4d177cb0",
      "Remove H.264 constrained baseline `42001f` (`H264ProfileLevelId42001fPacketizationMode0CodecParameters` and "
      "`...Mode1...` in livekit/protocol's `codecs` package) from the default enabled video codecs: it is "
      "incompatible with rust-sdk (openh264). Users can still enable the profile explicitly, if they are certain "
      "all devices support it, by naming its exact fmtp in the codec config, the way the H.264 main profile "
      "(`4d001f`) is enabled; a mime-only codec entry must not enable it."),
    t("lk-red-depacketize", "mediatransportutil", "livekit", LM, LK, "dd8e31a", "dd8e31a", "708b8ea6",
      "Faster-than-realtime replay export needs to recover primary Opus (and lost packets) from stored audio/red "
      "streams, but the RFC 2198 RED decoder lives inside livekit-server's `RedPrimaryReceiver`. Move it into a "
      "shared package in livekit/mediatransportutil, `pkg/red`, so the SFU and replay export share one "
      "implementation, with behavior unchanged from the SFU's; livekit-server drops its local copies and uses the "
      "package.\n\n`pkg/red` API: `ExtractPrimaryEncoding(payload []byte) ([]byte, error)`, "
      "`ExtractPackets(pkt *rtp.Packet, recoverBits byte) ([]*rtp.Packet, error)`, and a `Depacketizer` (zero value "
      "ready) with `(*Depacketizer).Depacketize(pkt *rtp.Packet) ([]*rtp.Packet, error)`, which tracks the last "
      "sequence number to recover packets lost before a RED packet from its redundant blocks."),
    t("lk-harden-inputs", "mediatransportutil", "livekit", LM, LK, "dec4ac0", "dec4ac0", "9a269325",
      "Harden the SFU and the room service against bad publisher input and a few races that reach code which does "
      "not expect them.\n\n- A VP9 packet with the 1-byte payload `0x08` and no dependency descriptor parses to an "
      "empty VP9 payload, and `IsVP9KeyFrame` (livekit/mediatransportutil `pkg/codec`) reads its first byte and "
      "panics.\n- A publisher can attach a dependency descriptor structure with fewer chains to a packet that a "
      "downtrack drops. `DDForwarder` keeps the old structure, and the writer reads past the new chain diffs.\n- A "
      "stream restart can clear the sender report between the report callback and its read, and the forwarder then "
      "uses the nil report (`Forwarder.SetRefSenderReport`).\n- A codec regression can race the dynacast manager "
      "closing, and the regression then uses a nil target quality (`HandleCodecRegression`).\n- A stored room can "
      "lack its internal record when a delete races a store, and `CreateRoom` then writes settings into the nil "
      "record.\n\nAlso in mediatransportutil: `parseH264SPS` should report a display size of 0 when the SPS crop is "
      "larger than the coded frame, as the H.265 path already does, instead of wrapping to a huge size."),
]
out = Path(__file__).resolve().parent / "real_tasks.json"
out.write_text(json.dumps(tasks, indent=1, ensure_ascii=False))
print(len(tasks), "tasks ->", out)
