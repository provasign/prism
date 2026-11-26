package mcp

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/provasign/prism/internal/core"
)

type rpcReply struct {
	ID     any            `json:"id"`
	Result map[string]any `json:"result"`
	Error  *core.RPCError `json:"error"`
}

func startServe(t *testing.T) (io.WriteCloser, chan rpcReply) {
	t.Helper()
	srv := NewServer(newTestHandler(t))
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = srv.Serve(inR, outW); outW.Close() }()
	replies := make(chan rpcReply, 16)
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			var r rpcReply
			if json.Unmarshal(sc.Bytes(), &r) == nil {
				replies <- r
			}
		}
		close(replies)
	}()
	t.Cleanup(func() { inW.Close() })
	return inW, replies
}

func send(t *testing.T, w io.Writer, s string) {
	t.Helper()
	if _, err := io.WriteString(w, s+"\n"); err != nil {
		t.Fatal(err)
	}
}

func next(t *testing.T, replies chan rpcReply, within time.Duration) rpcReply {
	t.Helper()
	select {
	case r := <-replies:
		return r
	case <-time.After(within):
		t.Fatalf("no reply within %v", within)
	}
	return rpcReply{}
}

// SEC-05: bad frames and bad JSON get an error reply and the session goes on.
func TestServeRejectsBadFramesAndKeepsServing(t *testing.T) {
	w, replies := startServe(t)
	if _, err := io.WriteString(w, "Content-Length: -5\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if r := next(t, replies, 5*time.Second); r.Error == nil || r.Error.Code != -32600 {
		t.Fatalf("negative length: %+v", r)
	}
	send(t, w, `{"jsonrpc":"2.0","id":1,"method":`)
	if r := next(t, replies, 5*time.Second); r.Error == nil || r.Error.Code != -32700 {
		t.Fatalf("malformed JSON: %+v", r)
	}
	send(t, w, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if r := next(t, replies, 5*time.Second); r.Error != nil || r.ID == nil {
		t.Fatalf("ping after bad input: %+v", r)
	}
}

// SEC-06: ping is answered while a slow call runs, and a cancelled call
// returns a cancellation error.
func TestServePingDuringSlowCallAndCancel(t *testing.T) {
	testToolCallDelay = 3 * time.Second
	defer func() { testToolCallDelay = 0 }()
	w, replies := startServe(t)
	send(t, w, `{"jsonrpc":"2.0","id":"slow","method":"tools/call","params":{"name":"prism_search","arguments":{"query":"x"}}}`)
	time.Sleep(50 * time.Millisecond)
	start := time.Now()
	send(t, w, `{"jsonrpc":"2.0","id":"p","method":"ping"}`)
	r := next(t, replies, 2*time.Second)
	if r.ID != "p" || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("ping blocked behind the slow call: %+v after %v", r, time.Since(start))
	}
	send(t, w, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"slow"}}`)
	r = next(t, replies, 2*time.Second)
	if r.ID != "slow" || r.Error == nil || !strings.Contains(r.Error.Message, "cancelled") {
		t.Fatalf("cancelled call: %+v", r)
	}
}

// SEC-06: concurrent tools/call against one real handler is race-free.
func TestServeConcurrentCallsRaceFree(t *testing.T) {
	h := newTextSearchHandler(t)
	mustWrite(t, h.Root, "a.go", "package a\n\nfunc Alpha() {}\nfunc Beta() { Alpha() }\n")
	srv := NewServer(h)
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	go func() { _ = srv.Serve(inR, outW); outW.Close() }()
	got := make(chan int, 1)
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		n := 0
		for sc.Scan() {
			n++
			if n == 8 {
				break
			}
		}
		got <- n
	}()
	for i := 0; i < 8; i++ {
		send(t, inW, `{"jsonrpc":"2.0","id":`+string(rune('0'+i))+`,"method":"tools/call","params":{"name":"prism_search","arguments":{"query":"Alpha","scope":"text"}}}`)
	}
	select {
	case n := <-got:
		if n != 8 {
			t.Fatalf("%d replies", n)
		}
	case <-time.After(60 * time.Second):
		t.Fatal("concurrent calls did not all answer")
	}
	inW.Close()
}
