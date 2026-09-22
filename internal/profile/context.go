package profile

import "context"

// filterCtxKey is the unexported context key type for the per-connection
// Filter. Using a private type avoids collisions with other packages.
type filterCtxKey struct{}

// filterMarker distinguishes "no filter stored" from "a nil filter was
// deliberately stored". A stored nil *Filter means filtering is inert
// (no profiles configured); the marker lets FilterFromContext tell that
// apart from a context that was never through the identity hook.
type filterMarker struct {
	filter *Filter
}

// WithFilter returns a context carrying the given filter. A nil filter
// is stored intentionally and signals "no filtering" (backward compat).
func WithFilter(ctx context.Context, f *Filter) context.Context {
	return context.WithValue(ctx, filterCtxKey{}, filterMarker{filter: f})
}

// FilterFromContext returns the filter stored in ctx, or nil if none was
// stored (or a nil filter was stored). A nil result means "do not
// filter" — backward compatible with the no-profiles case.
func FilterFromContext(ctx context.Context) *Filter {
	m, ok := ctx.Value(filterCtxKey{}).(filterMarker)
	if !ok {
		return nil
	}
	return m.filter
}
