// google-gmail handler is a persistent plugin for Gmail.
package main

// blah

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"

	googleauth "github.com/LeGambiArt/wtmcp/internal/google"
	"github.com/LeGambiArt/wtmcp/pkg/handler"
)

var gmailSvc *gmail.Service

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
			"token-gmail.json",
			[]string{"https://www.googleapis.com/auth/gmail.modify"},
		)
		if err != nil {
			return fmt.Errorf("google auth: %w", err)
		}

		svc, err := gmail.NewService(context.Background(), option.WithHTTPClient(client))
		if err != nil {
			return fmt.Errorf("gmail service: %w", err)
		}
		gmailSvc = svc
		return nil
	})

	p.Handle("gmail_list_messages", toolListMessages)
	p.Handle("gmail_get_messages_summary", toolGetMessagesSummary)
	p.Handle("gmail_fetch_and_cache", toolFetchAndCache)
	p.Handle("gmail_get_messages", toolGetMessages)
	p.Handle("gmail_send_message", toolSendMessage)
	p.Handle("gmail_create_draft", toolCreateDraft)
	p.Handle("gmail_modify_labels", toolModifyLabels)
	p.Handle("gmail_list_labels", toolListLabels)

	if err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "handler: %v\n", err)
		os.Exit(1)
	}
}
