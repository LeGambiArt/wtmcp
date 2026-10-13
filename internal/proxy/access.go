package proxy

import (
	"context"
	"net/http"
)

type toolAccessKey struct{}

// WithToolAccess attaches a tool's access level ("read" or "write")
// to the context. The proxy reads this to enforce HTTP method
// restrictions on read-only tools.
func WithToolAccess(ctx context.Context, access string) context.Context {
	return context.WithValue(ctx, toolAccessKey{}, access)
}

// ToolAccessFromContext extracts the tool's access level from context.
// Returns "" if no access level was set (e.g., during plugin init).
func ToolAccessFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(toolAccessKey{}).(string); ok {
		return v
	}
	return ""
}

type localWriteKey struct{}

// WithLocalWrite attaches the tool's local_write permission to the
// context. When true, access: read tools can issue file_write
// operations to the plugin's output directory.
func WithLocalWrite(ctx context.Context, localWrite bool) context.Context {
	return context.WithValue(ctx, localWriteKey{}, localWrite)
}

// LocalWriteFromContext extracts the local_write permission.
// Returns false if not set.
func LocalWriteFromContext(ctx context.Context) bool {
	v, _ := ctx.Value(localWriteKey{}).(bool)
	return v
}

type dryRunKey struct{}

// WithDryRun marks the context as a dry-run preview call. Cache
// writes are blocked for dry-run calls but allowed for normal
// read-only tools that use caching.
func WithDryRun(ctx context.Context, v bool) context.Context {
	return context.WithValue(ctx, dryRunKey{}, v)
}

// IsDryRun returns true if the context is marked as a dry-run call.
func IsDryRun(ctx context.Context) bool {
	v, _ := ctx.Value(dryRunKey{}).(bool)
	return v
}

var readOnlyMethods = map[string]bool{
	http.MethodGet:     true,
	http.MethodHead:    true,
	http.MethodOptions: true,
}

// isReadOnlyMethod returns true if the HTTP method is safe for
// read-only tools.
func isReadOnlyMethod(method string) bool {
	return readOnlyMethods[method]
}
