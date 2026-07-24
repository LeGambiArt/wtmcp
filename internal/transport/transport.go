// Package transport encapsulates MCP transport selection (stdio vs streamable-http).
package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"time"

	mcpserver "github.com/mark3labs/mcp-go/server"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

const shutdownTimeout = 5 * time.Second

// ListenAndServe starts the MCP server using the configured transport.
// For stdio, stdin/stdout are the reader/writer (pass os.Stdin/os.Stdout
// from main; tests pass pipes). For streamable-http, they are ignored.
// Blocks until ctx is cancelled or a startup error occurs.
func ListenAndServe(ctx context.Context, srv *mcpserver.MCPServer, cfg *config.ServerConfig, logger *slog.Logger, stdin io.Reader, stdout io.Writer) error {
	switch cfg.Transport {
	case config.TransportStdio:
		return listenStdio(ctx, srv, stdin, stdout)
	case config.TransportStreamableHTTP:
		return listenHTTP(ctx, srv, cfg, logger)
	default:
		return fmt.Errorf("unsupported transport: %q", cfg.Transport)
	}
}

func listenStdio(ctx context.Context, srv *mcpserver.MCPServer, stdin io.Reader, stdout io.Writer) error {
	stdioSrv := mcpserver.NewStdioServer(srv)
	stdioSrv.SetErrorLogger(log.Default())

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

func listenHTTP(ctx context.Context, srv *mcpserver.MCPServer, cfg *config.ServerConfig, logger *slog.Logger) error {
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))

	mux := http.NewServeMux()

	httpSrv := mcpserver.NewStreamableHTTPServer(srv,
		mcpserver.WithSessionIdleTTL(30*time.Minute),
		mcpserver.WithStreamableHTTPLogger(logger),
		mcpserver.WithStreamableHTTPServer(&http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadHeaderTimeout: 10 * time.Second,
		}),
	)

	mux.Handle("/mcp", httpSrv)
	mux.HandleFunc("/healthz", handleHealthz)

	errCh := make(chan error, 1)
	go func() {
		errCh <- httpSrv.Start(addr)
	}()

	listenURL := fmt.Sprintf("http://%s/mcp", addr)
	logger.Info("wtmcp listening", "url", listenURL)

	select {
	case err := <-errCh:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			logger.Error("http shutdown error", "err", err)
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
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	return fmt.Sprintf("http://%s/mcp", addr)
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"}) //nolint:errcheck,gosec // best-effort
}
