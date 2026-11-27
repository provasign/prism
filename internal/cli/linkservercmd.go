package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/provasign/prism/internal/linkserver"
)

const linkServerUsage = `usage:
  prism link-server <storage> [serve options]
  prism link-server token create --tokens <file> --name <name> [--upload <repo-glob>]... [--read <repo-glob>]... [--admin] [--expires 90d]
  prism link-server token list   --tokens <file>
  prism link-server token revoke --tokens <file> <id>
  prism link-server prune   <storage> [--repo <name>] [--keep N]
  prism link-server delete  <storage> --repo <name> [--sha <commit>]
  prism link-server check   <storage>
  prism link-server backup  <storage> --to <dir>
  prism link-server restore <storage> --from <dir>

storage, one of:
  --dir <store>                                   a local directory (one host)
  --s3 s3://bucket[/prefix] [--s3-endpoint URL] [--s3-region R] [--s3-path-style]
      AWS S3 or any S3-compatible store (MinIO, Ceph RGW, StorageGRID, ECS, ...)
      credentials: AWS_ACCESS_KEY_ID/AWS_SECRET_ACCESS_KEY[/AWS_SESSION_TOKEN], or AWS_ROLE_ARN + AWS_WEB_IDENTITY_TOKEN_FILE
  --azure https://<account>.blob.core.windows.net/<container>[/prefix] [--azure-path-style]
      Azure Blob Storage, Azure Stack Hub, Azurite
      credentials: AZURE_STORAGE_KEY, AZURE_STORAGE_SAS_TOKEN, AZURE_FEDERATED_TOKEN_FILE or AZURE_CLIENT_SECRET
      (+ AZURE_CLIENT_ID, AZURE_TENANT_ID), or AZURE_USE_MANAGED_IDENTITY=1
  --gcs gs://bucket[/prefix] [--gcs-endpoint URL]
      Google Cloud Storage; credentials: GOOGLE_APPLICATION_CREDENTIALS, else the metadata server
      (GKE workload identity, GCE, Cloud Run); STORAGE_EMULATOR_HOST for an emulator

serve options:
  --tokens <file>        per-token credentials (see token create); else PRISM_LINK_TOKEN, one shared token
  --no-auth              serve without credentials (refused otherwise)
  --addr <host>          listen address (default: every interface)   --port N (default 8890)
  --tls-cert F --tls-key F [--client-ca F]   HTTPS, and mTLS with --client-ca
  --ops-addr host:port   plain-HTTP /health and /metrics (for probes and scraping)
  --audit-log <file|->   one JSON line per request (default: stdout)
  --rate N               requests/s per token, burst 2N (default 50; 0 = off)
  --max-conns N          concurrent connections (default 512)
  --max-bundle-mb N      upload size limit (default 512)
  --keep N               commits kept per repository (default 20; 0 = keep all)
  --refresh D            re-read other replicas' uploads every D (default 10s)
  --trust-proxy          take client addresses from X-Forwarded-For
  --single-replica       run on a shared store that ignores conditional writes (start-up checks
                         them and refuses such a store otherwise); never run a second replica on it
Guide: docs/LINK_SERVER.md`

func linkServerUsageErr(msg string) int {
	if msg != "" {
		fmt.Fprintln(os.Stderr, "link-server:", msg)
	}
	fmt.Fprintln(os.Stderr, linkServerUsage)
	return 2
}

// storageFlags select the backend.
type storageFlags struct {
	dir, s3, s3Endpoint, s3Region string
	azure, gcs, gcsEndpoint       string
	s3PathStyle, azurePathStyle   bool
}

// take consumes a storage flag; ok is false when the flag is not one.
func (sf *storageFlags) take(it *argIter) (handled bool, rc int) {
	switch it.Flag {
	case "--dir", "--s3", "--s3-endpoint", "--s3-region", "--azure", "--gcs", "--gcs-endpoint":
		v, err := it.value()
		if err != nil {
			return true, usageError(err)
		}
		switch it.Flag {
		case "--dir":
			sf.dir = v
		case "--s3":
			sf.s3 = v
		case "--s3-endpoint":
			sf.s3Endpoint = v
		case "--s3-region":
			sf.s3Region = v
		case "--azure":
			sf.azure = v
		case "--gcs":
			sf.gcs = v
		case "--gcs-endpoint":
			sf.gcsEndpoint = v
		}
		return true, -1
	case "--s3-path-style":
		sf.s3PathStyle = true
		return true, -1
	case "--azure-path-style":
		sf.azurePathStyle = true
		return true, -1
	}
	return false, -1
}

func (sf *storageFlags) open(ctx context.Context) (linkserver.Blob, error) {
	n := 0
	for _, v := range []string{sf.dir, sf.s3, sf.azure, sf.gcs} {
		if v != "" {
			n++
		}
	}
	switch {
	case n > 1:
		return nil, errors.New("pass one of --dir, --s3, --azure or --gcs")
	case sf.dir != "":
		return linkserver.OpenFS(sf.dir)
	case sf.s3 != "":
		return linkserver.OpenS3(ctx, linkserver.S3Config{URL: sf.s3, Endpoint: sf.s3Endpoint, Region: sf.s3Region, PathStyle: sf.s3PathStyle})
	case sf.azure != "":
		return linkserver.OpenAzure(ctx, linkserver.AzureConfig{URL: sf.azure, PathStyle: sf.azurePathStyle})
	case sf.gcs != "":
		return linkserver.OpenGCS(ctx, linkserver.GCSConfig{URL: sf.gcs, Endpoint: sf.gcsEndpoint})
	}
	return nil, errors.New("no storage: pass --dir <store>, --s3 s3://bucket[/prefix], --azure https://<account>.blob.core.windows.net/<container>[/prefix] or --gcs gs://bucket[/prefix]")
}

func cmdLinkServer(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "token":
			return linkServerToken(args[1:])
		case "prune", "delete", "check", "backup", "restore":
			return linkServerAdmin(args[0], args[1:])
		case "help":
			fmt.Println(linkServerUsage)
			return 0
		}
	}
	var sf storageFlags
	addr, port := "", 8890
	var lc linkserver.ListenConfig
	tokens, auditPath := "", "-"
	noAuth, trustProxy, singleReplica := false, false, false
	rate, keep, maxMB := 50.0, linkserver.DefaultKeep, 512
	refresh := 10 * time.Second
	it := newArgIter("link-server", args)
	for it.next() {
		if handled, rc := sf.take(it); handled {
			if rc >= 0 {
				return rc
			}
			continue
		}
		switch it.Flag {
		case "--addr":
			v, ok := it.rawValue()
			if !ok || v == "" || strings.ContainsAny(v, "/ ") {
				fmt.Fprintln(os.Stderr, "link-server: --addr needs a host or IP address to listen on, such as 127.0.0.1")
				return 2
			}
			addr = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
		case "--port":
			p, err := it.positiveInt()
			if err != nil {
				return usageError(err)
			}
			port = p
		case "--tokens", "--audit-log", "--tls-cert", "--tls-key", "--client-ca", "--ops-addr", "--rate", "--max-conns", "--max-bundle-mb", "--keep", "--refresh":
			flag := it.Flag
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			switch flag {
			case "--tokens":
				tokens = v
			case "--audit-log":
				auditPath = v
			case "--tls-cert":
				lc.TLSCert = v
			case "--tls-key":
				lc.TLSKey = v
			case "--client-ca":
				lc.ClientCA = v
			case "--ops-addr":
				lc.OpsAddr = v
			case "--rate":
				f, err := strconv.ParseFloat(v, 64)
				if err != nil || f < 0 {
					return linkServerUsageErr("--rate needs a number >= 0")
				}
				rate = f
			case "--refresh":
				d, err := time.ParseDuration(v)
				if err != nil || d < 0 {
					return linkServerUsageErr("--refresh needs a duration such as 10s")
				}
				refresh = d
			default:
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 || (n == 0 && flag != "--keep") {
					return linkServerUsageErr(flag + " needs a whole number")
				}
				switch flag {
				case "--max-conns":
					lc.MaxConns = n
				case "--max-bundle-mb":
					maxMB = n
				case "--keep":
					keep = n
				}
			}
		case "--no-auth":
			noAuth = true
		case "--trust-proxy":
			trustProxy = true
		case "--single-replica":
			singleReplica = true
		default:
			return it.unknown()
		}
	}
	if len(it.Pos) > 0 {
		return rejectUnknownFlag("link-server", it.Pos[0])
	}
	if lc.ClientCA != "" && lc.TLSCert == "" {
		return linkServerUsageErr("--client-ca needs --tls-cert and --tls-key")
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	blob, err := sf.open(ctx)
	if err != nil {
		return linkServerUsageErr(err.Error())
	}
	if sf.dir == "" && !sharedStoreUsable(ctx, blob, singleReplica, logger) {
		return 1
	}
	store, err := linkserver.OpenStore(ctx, blob, linkserver.Options{Keep: keep})
	if err != nil {
		fmt.Fprintln(os.Stderr, "link-server:", err)
		return 1
	}

	auth, rc := linkServerAuth(tokens, noAuth, logger)
	if rc >= 0 {
		return rc
	}
	audit, rc := linkServerAudit(auditPath, logger)
	if rc >= 0 {
		return rc
	}
	srv := linkserver.NewServer(store, linkserver.ServerOptions{Auth: auth, Audit: audit, Log: logger, Rate: rate,
		MaxBundleBytes: int64(maxMB) << 20, TrustProxy: trustProxy})
	go store.Run(ctx, refresh, func(err error) { logger.Error("catalog refresh failed", "err", err) })

	lc.Addr = net.JoinHostPort(addr, strconv.Itoa(port))
	scheme := "http"
	if lc.TLSCert != "" {
		scheme = "https"
		if lc.ClientCA != "" {
			scheme = "https+mTLS"
		}
	}
	logger.Info("prism link server", "listen", lc.Addr, "scheme", scheme, "store", store.Describe(), "auth", auth.Describe(),
		"ops", lc.OpsAddr, "keep", keep, "rate", rate, "refresh", refresh.String())
	// Kept for scripts that read the v1.3 start-up line.
	fmt.Fprintf(os.Stderr, "prism link server on %s: %s\n", lc.Addr, store.Describe())
	if err := linkserver.Serve(ctx, srv, lc); err != nil {
		fmt.Fprintln(os.Stderr, "link-server:", err)
		return 1
	}
	return 0
}

func parseExpiry(v string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(v, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("bad --expires %q", v)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("bad --expires %q (use 90d or 720h)", v)
	}
	return d, nil
}

func linkServerToken(args []string) int {
	if len(args) == 0 {
		return linkServerUsageErr("token needs create, list or revoke")
	}
	sub := args[0]
	var file string
	var spec linkserver.TokenSpec
	it := newArgIter("link-server token", args[1:])
	for it.next() {
		switch it.Flag {
		case "--tokens", "--name", "--upload", "--read", "--expires":
			flag := it.Flag
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			switch flag {
			case "--tokens":
				file = v
			case "--name":
				spec.Name = v
			case "--upload":
				spec.Upload = append(spec.Upload, v)
			case "--read":
				spec.Read = append(spec.Read, v)
			case "--expires":
				d, err := parseExpiry(v)
				if err != nil {
					return linkServerUsageErr(err.Error())
				}
				spec.Expires = d
			}
		case "--admin":
			spec.Admin = true
		default:
			return it.unknown()
		}
	}
	if file == "" {
		return linkServerUsageErr("token " + sub + " needs --tokens <file>")
	}
	switch sub {
	case "create":
		secret, t, err := linkserver.CreateToken(file, spec)
		if err != nil {
			fmt.Fprintln(os.Stderr, "link-server token create:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "created token %s (%s); the secret below is shown once and stored only as a hash:\n", t.ID, t.Name)
		fmt.Println(secret)
		return 0
	case "list":
		ts, err := linkserver.ListTokens(file)
		if err != nil {
			fmt.Fprintln(os.Stderr, "link-server token list:", err)
			return 1
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tUPLOAD\tREAD\tADMIN\tCREATED\tEXPIRES")
		for _, t := range ts {
			exp := t.Expires
			if exp == "" {
				exp = "never"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%v\t%s\t%s\n", t.ID, t.Name, strings.Join(t.Upload, ","), strings.Join(t.Read, ","), t.Admin, t.Created, exp)
		}
		tw.Flush()
		return 0
	case "revoke":
		if len(it.Pos) != 1 {
			return linkServerUsageErr("token revoke needs one token id")
		}
		if err := linkserver.RevokeToken(file, it.Pos[0]); err != nil {
			fmt.Fprintln(os.Stderr, "link-server token revoke:", err)
			return 1
		}
		fmt.Printf("revoked %s; a running server stops accepting it within a second\n", it.Pos[0])
		return 0
	}
	return linkServerUsageErr("token needs create, list or revoke")
}

func linkServerAdmin(sub string, args []string) int {
	var sf storageFlags
	repo, sha, to, from := "", "", "", ""
	keep := linkserver.DefaultKeep
	asJSON := false
	it := newArgIter("link-server "+sub, args)
	for it.next() {
		if handled, rc := sf.take(it); handled {
			if rc >= 0 {
				return rc
			}
			continue
		}
		switch it.Flag {
		case "--repo", "--sha", "--to", "--from", "--keep":
			flag := it.Flag
			v, err := it.value()
			if err != nil {
				return usageError(err)
			}
			switch flag {
			case "--repo":
				repo = v
			case "--sha":
				sha = v
			case "--to":
				to = v
			case "--from":
				from = v
			case "--keep":
				n, err := strconv.Atoi(v)
				if err != nil || n < 1 {
					return linkServerUsageErr("--keep needs a whole number >= 1")
				}
				keep = n
			}
		case "--json":
			asJSON = true
		default:
			return it.unknown()
		}
	}
	ctx := context.Background()
	blob, err := sf.open(ctx)
	if err != nil {
		return linkServerUsageErr(err.Error())
	}
	switch sub {
	case "backup":
		if to == "" {
			return linkServerUsageErr("backup needs --to <dir>")
		}
		dst, err := linkserver.OpenFS(to)
		if err != nil {
			fmt.Fprintln(os.Stderr, "link-server backup:", err)
			return 1
		}
		n, err := linkserver.Copy(ctx, blob, dst)
		if err != nil {
			fmt.Fprintln(os.Stderr, "link-server backup:", err)
			return 1
		}
		fmt.Printf("backed up %d objects from %s to %s\n", n, blob.Describe(), to)
		return 0
	case "restore":
		if from == "" {
			return linkServerUsageErr("restore needs --from <dir>")
		}
		src, err := linkserver.OpenFS(from)
		if err != nil {
			fmt.Fprintln(os.Stderr, "link-server restore:", err)
			return 1
		}
		n, err := linkserver.Copy(ctx, src, blob)
		if err != nil {
			fmt.Fprintln(os.Stderr, "link-server restore:", err)
			return 1
		}
		fmt.Printf("restored %d objects from %s to %s; run prism link-server check on it\n", n, from, blob.Describe())
		return 0
	}
	store, err := linkserver.OpenStore(ctx, blob, linkserver.Options{})
	if err != nil {
		fmt.Fprintln(os.Stderr, "link-server "+sub+":", err)
		return 1
	}
	switch sub {
	case "check":
		repos, bundles, problems, err := store.Check(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "link-server check:", err)
			return 1
		}
		if asJSON {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"repositories": repos, "bundles": bundles, "problems": problems})
		} else {
			fmt.Printf("%d repositories, %d bundles, %d problems in %s\n", repos, bundles, len(problems), blob.Describe())
			for _, p := range problems {
				fmt.Printf("  %s: %s\n", p.Key, p.Issue)
			}
		}
		if len(problems) > 0 {
			return 1
		}
		return 0
	case "delete":
		if repo == "" {
			return linkServerUsageErr("delete needs --repo <name>")
		}
		if sha != "" {
			if err := store.DeleteCommit(ctx, repo, sha); err != nil {
				fmt.Fprintln(os.Stderr, "link-server delete:", err)
				return 1
			}
			fmt.Printf("deleted %s@%s\n", repo, sha)
			return 0
		}
		n, err := store.DeleteRepo(ctx, repo)
		if err != nil {
			fmt.Fprintln(os.Stderr, "link-server delete:", err)
			return 1
		}
		fmt.Printf("deleted %s (%d objects)\n", repo, n)
		return 0
	case "prune":
		repos := []string{repo}
		if repo == "" {
			repos = nil
			for _, r := range store.Repos() {
				repos = append(repos, r.Repo)
			}
		}
		total := 0
		for _, r := range repos {
			d, err := store.Prune(ctx, r, keep)
			if err != nil {
				fmt.Fprintln(os.Stderr, "link-server prune:", err)
				return 1
			}
			if len(d) > 0 {
				fmt.Printf("%s: deleted %d commits\n", r, len(d))
			}
			total += len(d)
		}
		fmt.Printf("pruned %d commits across %d repositories (keeping %d per repository)\n", total, len(repos), keep)
		return 0
	}
	return linkServerUsageErr("")
}

// linkServerAuth picks the credential model; rc >= 0 is an exit code.
func linkServerAuth(tokens string, noAuth bool, logger *slog.Logger) (linkserver.Authenticator, int) {
	var auth linkserver.Authenticator
	shared := linkserver.ClientToken()
	switch {
	case tokens != "":
		ta, err := linkserver.OpenTokenFile(tokens)
		if err != nil {
			fmt.Fprintln(os.Stderr, "link-server: --tokens:", err)
			return nil, 1
		}
		if shared != "" {
			ta.Fallback = linkserver.SharedToken(shared)
			logger.Warn("PRISM_LINK_TOKEN is also accepted: one shared secret with every right; unset it once every client has its own token")
		}
		auth = ta
	case shared != "":
		auth = linkserver.SharedToken(shared)
		logger.Warn("one shared token (PRISM_LINK_TOKEN) with every right; use --tokens for per-pipeline and per-team tokens")
	case noAuth:
		auth = linkserver.OpenAuth{}
		logger.Warn("NO AUTHENTICATION: anyone who can reach this port can read and upload bundles")
	default:
		return nil, linkServerUsageErr("no credentials: pass --tokens <file> (see `prism link-server token create`), set PRISM_LINK_TOKEN, or pass --no-auth")
	}

	return auth, -1
}

// linkServerAudit opens the audit destination ("-" stdout, "off", or a
// file reopened on SIGHUP); rc >= 0 is an exit code.
func linkServerAudit(auditPath string, logger *slog.Logger) (io.Writer, int) {
	var audit io.Writer = os.Stdout
	var reopen func() error
	switch auditPath {
	case "-", "":
	case "off":
		audit = nil
	default:
		f, err := linkserver.OpenAuditFile(auditPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "link-server: --audit-log:", err)
			return nil, 1
		}
		audit, reopen = f, f.Reopen
	}
	if reopen != nil {
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		go func() {
			for range hup {
				if err := reopen(); err != nil {
					logger.Error("audit log reopen failed", "err", err)
				}
			}
		}()
	}

	return audit, -1
}

// sharedStoreUsable checks a shared store honours compare-and-swap, or
// replicas lose each other's branch updates without an error. A store that
// ignores it is accepted only with --single-replica.
func sharedStoreUsable(ctx context.Context, blob linkserver.Blob, singleReplica bool, logger *slog.Logger) bool {
	err := linkserver.VerifyConditionalWrites(ctx, blob)
	if err == nil {
		return true
	}
	if singleReplica && errors.Is(err, linkserver.ErrNoConditionalWrites) {
		logger.Warn("store without conditional writes: safe only as the single replica (--single-replica)", "store", blob.Describe())
		return true
	}
	fmt.Fprintf(os.Stderr, "link-server: %s: %v\n  Use a store that supports conditional writes, or run exactly one replica with --single-replica.\n", blob.Describe(), err)
	return false
}
