package linkserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Token is the bearer token a client sends and a server requires:
// $PRISM_LINK_TOKEN. It is never read from a repository file, so it cannot
// be committed by accident.
func Token() string { return strings.TrimSpace(os.Getenv("PRISM_LINK_TOKEN")) }

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
	if t := Token(); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(req)
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

// AskDefine asks a server where key is declared.
func AskDefine(ctx context.Context, server, key string) ([]RepoDefs, error) {
	var resp struct {
		Repos []RepoDefs `json:"repos"`
	}
	err := post(ctx, server, "/link/define", map[string]string{"key": key}, &resp)
	return resp.Repos, err
}

// AskDefineName asks a server where a symbol with this name is declared.
func AskDefineName(ctx context.Context, server, name string) ([]RepoDefs, error) {
	var resp struct {
		Repos []RepoDefs `json:"repos"`
	}
	err := post(ctx, server, "/link/define", map[string]string{"name": name}, &resp)
	return resp.Repos, err
}
