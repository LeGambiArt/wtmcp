package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/mark3labs/mcp-go/mcp"
)

// OutputFramer wraps plugin tool results with data annotations and
// optional nonce-based text tags for prompt injection defense.
type OutputFramer struct {
	nonce    string
	tagRegex *regexp.Regexp
	tagText  bool
	sanitize bool
}

// newOutputFramer creates a framer with a per-session nonce.
// tagText controls whether nonce-based text tags are added
// (secondary defense layer, opt-in).
func newOutputFramer(tagText, sanitize bool) (*OutputFramer, error) {
	nonceBytes := make([]byte, 8) // 16 hex chars = 64 bits
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)

	tagRegex := regexp.MustCompile(
		fmt.Sprintf(`(?i)<\s*/?\s*tool-result-%s[^>]*>`, nonce),
	)

	return &OutputFramer{
		nonce:    nonce,
		tagRegex: tagRegex,
		tagText:  tagText,
		sanitize: sanitize,
	}, nil
}

// frameToolResult creates a CallToolResult with Annotations.Audience
// set to [RoleAssistant] on the TextContent, and optionally wraps
// the text in nonce-based data tags. Nil-safe: returns a plain text
// result when framer is nil.
func (f *OutputFramer) frameToolResult(toolName, text string) *mcp.CallToolResult {
	if f == nil {
		return mcp.NewToolResultText(sanitizeContent(text))
	}
	if f.sanitize {
		text = sanitizeContent(text)
	}
	if f.tagText {
		text = f.wrapToolOutput(toolName, text)
	}

	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Annotated: mcp.Annotated{
					Annotations: &mcp.Annotations{
						Audience: []mcp.Role{mcp.RoleAssistant},
					},
				},
				Type: "text",
				Text: text,
			},
		},
	}
}

// frameErrorResult creates a CallToolResult with IsError=true and
// Annotations.Audience=[RoleAssistant]. Error messages may contain
// attacker-controlled content from external API responses, so the
// audience annotation ensures clients treat them as model-consumption
// data rather than user-facing output.
func frameErrorResult(text string) *mcp.CallToolResult {
	text = sanitizeContent(text)
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{
			mcp.TextContent{
				Annotated: mcp.Annotated{
					Annotations: &mcp.Annotations{
						Audience: []mcp.Role{mcp.RoleAssistant},
					},
				},
				Type: "text",
				Text: text,
			},
		},
	}
}

// sanitizedTextResult creates a CallToolResult with sanitized text
// and Audience=[RoleAssistant]. Used for management tool output that
// may contain externally-sourced strings (e.g. plugin error reasons).
func sanitizedTextResult(text string) *mcp.CallToolResult {
	text = sanitizeContent(text)
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			mcp.TextContent{
				Annotated: mcp.Annotated{
					Annotations: &mcp.Annotations{
						Audience: []mcp.Role{mcp.RoleAssistant},
					},
				},
				Type: "text",
				Text: text,
			},
		},
	}
}

func (f *OutputFramer) wrapToolOutput(toolName, text string) string {
	safe := f.tagRegex.ReplaceAllString(text, "[escaped]")
	return fmt.Sprintf("<tool-result-%s source=%q type=\"data\">\n%s\n</tool-result-%s>",
		f.nonce, toolName, safe, f.nonce)
}
