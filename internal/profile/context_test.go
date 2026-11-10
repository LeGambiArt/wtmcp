package profile

import (
	"context"
	"testing"

	"github.com/LeGambiArt/wtmcp/internal/config"
)

func TestContextRoundtrip(t *testing.T) {
	f := mustCompile(t, "p", config.ProfileDefinition{
		Allow: map[string][]string{"gitlab": {"gitlab_get_.*"}},
	})
	ctx := WithFilter(context.Background(), f)
	got := FilterFromContext(ctx)
	if got == nil || got.Name() != "p" {
		t.Errorf("FilterFromContext = %v, want filter named p", got)
	}
}

func TestContextNoFilter(t *testing.T) {
	if got := FilterFromContext(context.Background()); got != nil {
		t.Errorf("empty context should return nil filter, got %v", got)
	}
}

func TestContextStoredNil(t *testing.T) {
	// Storing a nil filter (no profiles configured) is distinct from a
	// context that never went through the identity hook, but both yield
	// nil from FilterFromContext (meaning "no filtering").
	ctx := WithFilter(context.Background(), nil)
	if got := FilterFromContext(ctx); got != nil {
		t.Errorf("stored nil filter should read back as nil, got %v", got)
	}
}
