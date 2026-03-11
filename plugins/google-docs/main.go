// google-docs handler is a persistent plugin for Google Docs.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"google.golang.org/api/docs/v1"
	"google.golang.org/api/option"

	googleauth "github.com/LeGambiArt/wtmcp/internal/google"
	"github.com/LeGambiArt/wtmcp/pkg/handler"
)

var docsSvc *docs.Service

func main() {
	p := handler.New()

	p.OnInit(func(cfgRaw json.RawMessage) error {
		var cfg map[string]string
		if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
			return fmt.Errorf("parse config: %w", err)
		}

		credDir := cfg["_credentials_dir"]
		if credDir == "" {
			credDir = googleauth.CredentialsDir()
		}

		client, err := googleauth.NewHTTPClientFromDir(
			context.Background(),
			credDir,
			"token-docs.json",
			[]string{"https://www.googleapis.com/auth/documents"},
		)
		if err != nil {
			return fmt.Errorf("google auth: %w", err)
		}

		svc, err := docs.NewService(context.Background(), option.WithHTTPClient(client))
		if err != nil {
			return fmt.Errorf("docs service: %w", err)
		}
		docsSvc = svc
		return nil
	})

	p.Handle("gdocs_get_document", toolGetDocument)
	p.Handle("gdocs_get_document_text", toolGetDocumentText)
	p.Handle("gdocs_get_document_markdown", toolGetDocumentMarkdown)
	p.Handle("gdocs_summarize_document", toolSummarizeDocument)
	p.Handle("gdocs_extract_and_get_from_text", toolExtractAndGet)
	p.Handle("gdocs_write_text", toolWriteText)
	p.Handle("gdocs_create_document", toolCreateDocument)

	if err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "handler: %v\n", err)
		os.Exit(1)
	}
}
