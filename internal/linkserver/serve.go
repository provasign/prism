package linkserver

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// ListenConfig is how the server listens.
type ListenConfig struct {
	Addr string // host:port
	// TLSCert and TLSKey turn on HTTPS. The files are re-read when they
	// change, so a certificate can be rotated without a restart.
	TLSCert, TLSKey string
	// ClientCA, with TLS, requires a client certificate signed by it (mTLS).
	ClientCA string
	// MaxConns caps concurrent connections; 0 means 512.
	MaxConns int
	// OpsAddr, when set, serves /health and /metrics over plain HTTP.
	OpsAddr string
}

// Serve runs the public listener (and the ops listener) until ctx ends,
// then shuts down gracefully, letting requests in flight finish.
func Serve(ctx context.Context, srv *Server, lc ListenConfig) error {
	public := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute, // large uploads
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
		ErrorLog:          nil,
	}
	ln, err := net.Listen("tcp", lc.Addr)
	if err != nil {
		return err
	}
	max := lc.MaxConns
	if max <= 0 {
		max = 512
	}
	ln = limitListener(ln, max)
	if lc.TLSCert != "" || lc.TLSKey != "" {
		cfg, err := serverTLS(lc)
		if err != nil {
			ln.Close()
			return err
		}
		ln = tls.NewListener(ln, cfg)
	}
	errc := make(chan error, 2)
	go func() { errc <- public.Serve(ln) }()
	var ops *http.Server
	if lc.OpsAddr != "" {
		ops = &http.Server{Addr: lc.OpsAddr, Handler: srv.OpsHandler(), ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
		go func() { errc <- ops.ListenAndServe() }()
	}
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			public.Close()
			if ops != nil {
				ops.Close()
			}
			return err
		}
	}
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if ops != nil {
		_ = ops.Shutdown(sctx)
	}
	return public.Shutdown(sctx)
}

func serverTLS(lc ListenConfig) (*tls.Config, error) {
	if lc.TLSCert == "" || lc.TLSKey == "" {
		return nil, errors.New("TLS needs both --tls-cert and --tls-key")
	}
	cr := &certReloader{cert: lc.TLSCert, key: lc.TLSKey}
	if _, err := cr.get(); err != nil {
		return nil, err
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return cr.get() }}
	if lc.ClientCA != "" {
		pool, err := loadPool(lc.ClientCA)
		if err != nil {
			return nil, err
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

type certReloader struct {
	cert, key string
	mu        sync.Mutex
	loaded    *tls.Certificate
	mod       time.Time
	checked   time.Time
}

func (c *certReloader) get() (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.loaded != nil && time.Since(c.checked) < 5*time.Second {
		return c.loaded, nil
	}
	c.checked = time.Now()
	ci, err1 := os.Stat(c.cert)
	ki, err2 := os.Stat(c.key)
	if err1 != nil || err2 != nil {
		if c.loaded != nil {
			return c.loaded, nil
		}
		return nil, fmt.Errorf("TLS certificate: %v %v", err1, err2)
	}
	mod := ci.ModTime()
	if ki.ModTime().After(mod) {
		mod = ki.ModTime()
	}
	if c.loaded != nil && mod.Equal(c.mod) {
		return c.loaded, nil
	}
	pair, err := tls.LoadX509KeyPair(c.cert, c.key)
	if err != nil {
		if c.loaded != nil {
			return c.loaded, nil // a half-rotated pair keeps the old one
		}
		return nil, err
	}
	c.loaded, c.mod = &pair, mod
	return c.loaded, nil
}

func loadPool(pemFile string) (*x509.CertPool, error) {
	return addPEM(x509.NewCertPool(), pemFile)
}

// systemPoolWith is the system's roots plus those in pemFile.
func systemPoolWith(pemFile string) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	return addPEM(pool, pemFile)
}

func addPEM(pool *x509.CertPool, pemFile string) (*x509.CertPool, error) {
	data, err := os.ReadFile(pemFile)
	if err != nil {
		return nil, err
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("%s holds no PEM certificates", pemFile)
	}
	return pool, nil
}

// limitListener caps concurrent connections; Accept waits for a free slot.
type limitedListener struct {
	net.Listener
	sem chan struct{}
}

func limitListener(l net.Listener, n int) net.Listener {
	return &limitedListener{Listener: l, sem: make(chan struct{}, n)}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	l.sem <- struct{}{}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.sem
		return nil, err
	}
	return &limitedConn{Conn: c, release: func() { <-l.sem }}, nil
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
