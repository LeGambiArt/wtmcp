// Package transport encapsulates MCP transport selection (stdio vs streamable-http).
package transport

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

const shutdownTimeout = 5 * time.Second

// Option configures optional transport behavior (identity context funcs
// used for profile filtering). Options are variadic so existing callers
// that do not use profiles are unaffected.
type Option func(*options)

type options struct {
	httpContextFunc  mcpserver.HTTPContextFunc
	stdioContextFunc mcpserver.StdioContextFunc
}

// WithHTTPContextFunc sets the per-request context function for the
// streamable-http transport (used to extract the client's TLS identity
// and store its resolved profile filter in the request context).
func WithHTTPContextFunc(fn mcpserver.HTTPContextFunc) Option {
	return func(o *options) { o.httpContextFunc = fn }
}

// WithStdioContextFunc sets the context function for the stdio transport
// (called once at Listen, used to inject a startup-resolved filter).
func WithStdioContextFunc(fn mcpserver.StdioContextFunc) Option {
	return func(o *options) { o.stdioContextFunc = fn }
}

// ListenAndServe starts the MCP server using the configured transport.
// For stdio, stdin/stdout are the reader/writer (pass os.Stdin/os.Stdout
// from main; tests pass pipes). For streamable-http, they are ignored.
// Blocks until ctx is cancelled or a startup error occurs.
func ListenAndServe(ctx context.Context, srv *mcpserver.MCPServer, cfg *config.ServerConfig, logger *slog.Logger, stdin io.Reader, stdout io.Writer, opts ...Option) error {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	switch cfg.Transport {
	case config.TransportStdio:
		return listenStdio(ctx, srv, stdin, stdout, o.stdioContextFunc)
	case config.TransportStreamableHTTP:
		return listenHTTP(ctx, srv, cfg, logger, o.httpContextFunc)
	default:
		return fmt.Errorf("unsupported transport: %q", cfg.Transport)
	}
}

func listenStdio(ctx context.Context, srv *mcpserver.MCPServer, stdin io.Reader, stdout io.Writer, contextFunc mcpserver.StdioContextFunc) error {
	stdioSrv := mcpserver.NewStdioServer(srv)
	stdioSrv.SetErrorLogger(log.Default())
	if contextFunc != nil {
		stdioSrv.SetContextFunc(contextFunc)
	}

	err := stdioSrv.Listen(ctx, stdin, stdout)
	if closer, ok := stdin.(io.Closer); ok {
		closer.Close() //nolint:errcheck,gosec // best-effort cleanup
	}

	// Context cancellation is expected (shutdown); suppress the error.
	if ctx.Err() != nil {
		return nil //nolint:nilerr // cancellation is not an error
	}
	return err
}

func listenHTTP(ctx context.Context, srv *mcpserver.MCPServer, cfg *config.ServerConfig, logger *slog.Logger, contextFunc mcpserver.HTTPContextFunc) error {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))

	mux := http.NewServeMux()

	httpServer := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	opts := []mcpserver.StreamableHTTPOption{
		mcpserver.WithSessionIdleTTL(30 * time.Minute),
		mcpserver.WithHeartbeatInterval(30 * time.Second),
		mcpserver.WithStreamableHTTPLogger(logger),
		mcpserver.WithStreamableHTTPServer(httpServer),
	}
	if contextFunc != nil {
		opts = append(opts, mcpserver.WithHTTPContextFunc(contextFunc))
	}

	tlsEnabled := cfg.TLS != nil && cfg.TLS.CertFile != ""
	scheme := "http"
	if tlsEnabled {
		tlsConfig, err := buildServerTLS(cfg.TLS)
		if err != nil {
			return fmt.Errorf("server TLS: %w", err)
		}
		httpServer.TLSConfig = tlsConfig
		opts = append(opts, mcpserver.WithTLSCert(cfg.TLS.CertFile, cfg.TLS.KeyFile))
		scheme = "https"
	}

	httpSrv := mcpserver.NewStreamableHTTPServer(srv, opts...)

	mux.Handle("/mcp", httpSrv)
	mux.HandleFunc("/healthz", handleHealthz)

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpSrv.Start(addr)
	}()

	if !tlsEnabled && cfg.Host != "localhost" && cfg.Host != "127.0.0.1" && cfg.Host != "::1" {
		logger.Warn("binding to non-loopback address with no authentication",
			"host", cfg.Host, "port", cfg.Port)
	}

	listenURL := fmt.Sprintf("%s://%s/mcp", scheme, addr)
	logger.Info("wtmcp starting", "transport", "streamable-http", "addr", listenURL)

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("http shutdown error, forcing close", "err", err)
		}
		// Start() is guaranteed to have returned after Shutdown() closes the listener.
		if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server: %w", err)
		}
		return nil
	}
}

// ListenURL returns the URL the HTTP server would listen on, for use
// in control info files. Returns "stdio" for stdio transport.
func ListenURL(cfg *config.ServerConfig) string {
	if cfg.Transport == config.TransportStdio {
		return "stdio"
	}
	scheme := "http"
	if cfg.TLS != nil && cfg.TLS.CertFile != "" {
		scheme = "https"
	}
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	return fmt.Sprintf("%s://%s/mcp", scheme, addr)
}

// buildServerTLS builds a *tls.Config for the streamable-http server
// from the configured cert/CA files and client-auth mode. The server
// certificate itself is loaded by mcp-go via WithTLSCert; this config
// carries the client CA pool and client-auth policy for mTLS.
func buildServerTLS(cfg *config.ServerTLSConfig) (*tls.Config, error) {
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}

	if cfg.CAFile != "" {
		caPEM, err := os.ReadFile(cfg.CAFile) //nolint:gosec // CA path from config
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("no valid certificates in CA file %s", cfg.CAFile)
		}
		tlsCfg.ClientCAs = pool
	}

	switch cfg.ClientAuth {
	case config.ClientAuthRequire:
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	case config.ClientAuthRequest:
		tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven
	default:
		tlsCfg.ClientAuth = tls.NoClientCert
	}

	return tlsCfg, nil
}

// handleHealthz returns 200 OK for Kubernetes liveness probes.
// This endpoint does not check plugin loading state — use it for
// liveness only, not readiness. A /readyz endpoint that checks
// plugin availability is planned for a future release.
func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck,gosec // best-effort
}
