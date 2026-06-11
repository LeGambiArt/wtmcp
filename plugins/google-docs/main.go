// google-docs handler is a persistent plugin for Google Docs.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"google.golang.org/api/docs/v1"
	"google.golang.org/api/option"

	"github.com/LeGambiArt/wtmcp/pkg/handler"
	"github.com/LeGambiArt/wtmcp/pkg/pathutil"
)

var (
	docsSvc            *docs.Service
	sessionDir         string
	resolvedSessionDir string
	plug               *handler.Plugin
)

func main() {
	p := handler.New()
	plug = p

	p.OnInit(func(cfgRaw json.RawMessage) error {
		client := handler.NewProxyTransport(p).Client()
		svc, err := docs.NewService(context.Background(), option.WithHTTPClient(client))
		if err != nil {
			return fmt.Errorf("docs service: %w", err)
		}
		docsSvc = svc

		var cfg map[string]string
		if err := json.Unmarshal(cfgRaw, &cfg); err == nil {
			sessionDir = cfg["_session_dir"]
			if sessionDir != "" {
				var err error
				resolvedSessionDir, err = pathutil.ResolvePath(sessionDir)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: resolve session dir: %v\n", err)
				}
			}
		}
		return nil
	})

	p.Handle("gdocs_get_document", toolGetDocument)
	p.Handle("gdocs_get_document_text", toolGetDocumentText)
	p.Handle("gdocs_get_document_markdown", toolGetDocumentMarkdown)
	p.Handle("gdocs_summarize_document", toolSummarizeDocument)
	p.Handle("gdocs_extract_and_get_from_text", toolExtractAndGet)
	p.Handle("gdocs_write", toolWrite)
	p.Handle("gdocs_create_document", toolCreateDocument)

	if err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "handler: %v\n", err)
		os.Exit(1)
	}
}
