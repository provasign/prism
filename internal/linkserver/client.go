package linkserver

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Token is the bearer token a client sends and a server requires:
// $PRISM_LINK_TOKEN. It is never read from a repository file, so it cannot
// be committed by accident.
func ClientToken() string { return strings.TrimSpace(os.Getenv("PRISM_LINK_TOKEN")) }

func post(ctx context.Context, server, path string, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if t := ClientToken(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	client, err := httpClient()
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return fmt.Errorf("%s: %s", resp.Status, e.Error)
	}
	return json.Unmarshal(data, out)
}

// Push uploads a bundle.
func Push(ctx context.Context, server string, b Bundle) error {
	var ack map[string]any
	return post(ctx, server, "/link/bundle", b, &ack)
}

// AskConsumers asks a server who references keys.
func AskConsumers(ctx context.Context, server string, req ConsumersRequest) (ConsumersResponse, error) {
	var resp ConsumersResponse
	err := post(ctx, server, "/link/consumers", req, &resp)
	return resp, err
}

// DefineAnswer is a define reply: the declaring repositories, and how many
// uploaded repositories the caller's token may not read (not searched).
type DefineAnswer struct {
	Repos      []RepoDefs `json:"repos"`
	Unreadable int        `json:"unreadable,omitempty"`
}

// AskDefine asks a server where key is declared.
func AskDefine(ctx context.Context, server, key string) (DefineAnswer, error) {
	var resp DefineAnswer
	err := post(ctx, server, "/link/define", map[string]string{"key": key}, &resp)
	return resp, err
}

// AskDefineName asks a server where a symbol with this name is declared.
func AskDefineName(ctx context.Context, server, name string) (DefineAnswer, error) {
	var resp DefineAnswer
	err := post(ctx, server, "/link/define", map[string]string{"name": name}, &resp)
	return resp, err
}

var (
	clientOnce sync.Once
	clientVal  *http.Client
	clientErr  error
)

// httpClient is the client every request uses. PRISM_LINK_CA adds trusted
// roots (a private CA) to the system's; PRISM_LINK_CLIENT_CERT and
// PRISM_LINK_CLIENT_KEY present a client certificate to a server that
// requires mTLS. Like the token, these come only from the environment.
func httpClient() (*http.Client, error) {
	clientOnce.Do(func() {
		cfg := &tls.Config{MinVersion: tls.VersionTLS12}
		if ca := strings.TrimSpace(os.Getenv("PRISM_LINK_CA")); ca != "" {
			pool, err := systemPoolWith(ca)
			if err != nil {
				clientErr = fmt.Errorf("PRISM_LINK_CA: %w", err)
				return
			}
			cfg.RootCAs = pool
		}
		cert, key := strings.TrimSpace(os.Getenv("PRISM_LINK_CLIENT_CERT")), strings.TrimSpace(os.Getenv("PRISM_LINK_CLIENT_KEY"))
		if cert != "" || key != "" {
			pair, err := tls.LoadX509KeyPair(cert, key)
			if err != nil {
				clientErr = fmt.Errorf("PRISM_LINK_CLIENT_CERT/KEY: %w", err)
				return
			}
			cfg.Certificates = []tls.Certificate{pair}
		}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.TLSClientConfig = cfg
		clientVal = &http.Client{Timeout: 60 * time.Second, Transport: tr}
	})
	return clientVal, clientErr
}
