// Command loadtest drives a running Prism link server with synthetic
// repositories and concurrent queries, and reports latency percentiles.
//
//	go run ./internal/linkserver/loadtest -server http://127.0.0.1:8890 -token "$T" \
//	    -repos 1000 -queries 10000 -concurrency 32
//
// Each synthetic repository references a random subset of a pool of library
// symbols (between -min-refs and -max-refs references) and declares -defs
// symbols of its own. Queries ask consumers of one random library symbol,
// or define one random name, in a 4:1 mix.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type site struct {
	Key  string `json:"key"`
	File string `json:"file"`
	From string `json:"from"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type def struct {
	Key, Kind, Name, Signature, File string
	Line                             int
}

type bundle struct {
	Format   int    `json:"format"`
	Repo     string `json:"repo"`
	SHA      string `json:"sha"`
	Ref      string `json:"ref"`
	Created  string `json:"created"`
	Refs     []site `json:"refs"`
	Defs     []def  `json:"defs"`
	Packages []any  `json:"packages"`
	Requires []any  `json:"requires"`
}

func main() {
	server := flag.String("server", "http://127.0.0.1:8890", "link server URL")
	token := flag.String("token", os.Getenv("PRISM_LINK_TOKEN"), "bearer token (upload and read)")
	repos := flag.Int("repos", 1000, "synthetic repositories to upload")
	pool := flag.Int("symbols", 2000, "library symbols referenced across repositories")
	minRefs := flag.Int("min-refs", 50, "fewest references per repository")
	maxRefs := flag.Int("max-refs", 2000, "most references per repository")
	defs := flag.Int("defs", 300, "declarations per repository")
	queries := flag.Int("queries", 10000, "queries to send")
	conc := flag.Int("concurrency", 32, "concurrent clients")
	skipUpload := flag.Bool("skip-upload", false, "query an already loaded server")
	seed := flag.Int64("seed", 1, "random seed")
	flag.Parse()
	rng := rand.New(rand.NewSource(*seed))
	client := &http.Client{Timeout: 2 * time.Minute, Transport: &http.Transport{MaxIdleConnsPerHost: *conc * 2}}
	post := func(path string, body []byte) (int, int, error) {
		req, _ := http.NewRequest(http.MethodPost, strings.TrimRight(*server, "/")+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+*token)
		resp, err := client.Do(req)
		if err != nil {
			return 0, 0, err
		}
		n, _ := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode, int(n), nil
	}

	if !*skipUpload {
		var bytesUp int64
		var refsUp int64
		start := time.Now()
		jobs := make(chan []byte, *conc)
		var wg sync.WaitGroup
		var failed atomic.Int64
		for w := 0; w < *conc; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for body := range jobs {
					code, _, err := post("/link/bundle", body)
					if err != nil || code != 200 {
						failed.Add(1)
						if failed.Load() <= 3 {
							fmt.Fprintf(os.Stderr, "upload failed: %d %v\n", code, err)
						}
					}
				}
			}()
		}
		for i := 0; i < *repos; i++ {
			n := *minRefs + rng.Intn(*maxRefs-*minRefs+1)
			b := bundle{Format: 1, Repo: fmt.Sprintf("git.example.com/org%02d/repo%04d", i%50, i), SHA: fmt.Sprintf("%040x", rng.Int63()),
				Ref: "main", Created: time.Now().UTC().Format(time.RFC3339)}
			for r := 0; r < n; r++ {
				k := rng.Intn(*pool)
				b.Refs = append(b.Refs, site{Key: fmt.Sprintf("go:git.example.com/lib/pkg%02d#Sym%04d", k%40, k),
					File: fmt.Sprintf("internal/p%d/f%d.go", r%17, r%101), From: fmt.Sprintf("Func%d", r%300), Line: 1 + rng.Intn(800),
					Text: fmt.Sprintf("v := pkg%02d.Sym%04d(ctx, arg%d)", k%40, k, r)})
			}
			for d := 0; d < *defs; d++ {
				b.Defs = append(b.Defs, def{Key: fmt.Sprintf("go:git.example.com/org%02d/repo%04d#T%d.M%d", i%50, i, d/10, d), Kind: "method",
					Name: fmt.Sprintf("T%d.M%d", d/10, d), Signature: fmt.Sprintf("func (t *T%d) M%d(ctx context.Context) error", d/10, d),
					File: fmt.Sprintf("t%d.go", d/10), Line: 10 + d})
			}
			data, _ := json.Marshal(b)
			bytesUp += int64(len(data))
			refsUp += int64(n)
			jobs <- data
		}
		close(jobs)
		wg.Wait()
		d := time.Since(start)
		fmt.Printf("upload: %d repositories, %d references, %.1f MB in %s (%.1f repos/s), %d failed\n",
			*repos, refsUp, float64(bytesUp)/1e6, d.Round(time.Millisecond), float64(*repos)/d.Seconds(), failed.Load())
	}

	type result struct {
		kind string
		dur  time.Duration
		ok   bool
		size int
	}
	results := make([]result, *queries)
	var next atomic.Int64
	var wg sync.WaitGroup
	start := time.Now()
	for w := 0; w < *conc; w++ {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			r := rand.New(rand.NewSource(seed))
			for {
				i := int(next.Add(1)) - 1
				if i >= *queries {
					return
				}
				var path string
				var body []byte
				kind := "consumers"
				if i%5 == 4 {
					kind = "define"
					d := r.Intn(*defs)
					body, _ = json.Marshal(map[string]string{"name": fmt.Sprintf("T%d.M%d", d/10, d)})
					path = "/link/define"
				} else {
					k := r.Intn(*pool)
					body, _ = json.Marshal(map[string]any{"keys": []string{fmt.Sprintf("go:git.example.com/lib/pkg%02d#Sym%04d", k%40, k)}})
					path = "/link/consumers"
				}
				t0 := time.Now()
				code, n, err := post(path, body)
				results[i] = result{kind: kind, dur: time.Since(t0), ok: err == nil && code == 200, size: n}
			}
		}(*seed + int64(w) + 1)
	}
	wg.Wait()
	total := time.Since(start)
	for _, kind := range []string{"consumers", "define"} {
		var ds []time.Duration
		bad, bytes := 0, 0
		for _, r := range results {
			if r.kind != kind {
				continue
			}
			ds = append(ds, r.dur)
			bytes += r.size
			if !r.ok {
				bad++
			}
		}
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		pct := func(p float64) time.Duration { return ds[int(p*float64(len(ds)-1))] }
		fmt.Printf("%-9s n=%d  p50=%s  p95=%s  p99=%s  max=%s  errors=%d  avg answer=%.1f KB\n", kind, len(ds),
			pct(.5).Round(time.Microsecond*100), pct(.95).Round(time.Microsecond*100), pct(.99).Round(time.Microsecond*100),
			ds[len(ds)-1].Round(time.Millisecond), bad, float64(bytes)/float64(len(ds))/1e3)
	}
	fmt.Printf("queries: %d in %s (%.0f/s) with %d clients\n", *queries, total.Round(time.Millisecond), float64(*queries)/total.Seconds(), *conc)
}
