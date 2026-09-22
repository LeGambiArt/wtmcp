package main

import (
	"context"
	"fmt"
	"log"
	"net/http"

	"github.com/LeGambiArt/wtmcp/internal/config"
	"github.com/LeGambiArt/wtmcp/internal/profile"
	"github.com/LeGambiArt/wtmcp/internal/transport"
)

// setupProfiles loads profiles.d, validates it, and builds a Resolver.
// Fatal validation errors abort startup (the same way an invalid
// config.yaml does); warnings are logged. When no profiles.d directory
// exists, the returned resolver is unconfigured and filtering is inert.
func setupProfiles(cfg *config.Config, workdir string) (*profile.Resolver, error) {
	profilesDir := config.ResolveProfilesDir(cfg, workdir)

	loaded, err := config.LoadProfiles(profilesDir)
	if err != nil {
		return nil, fmt.Errorf("load profiles: %w", err)
	}

	problems := profile.Validate(loaded, cfg.Profiles.Default)
	var fatal bool
	for _, p := range problems {
		prefix := "WARNING"
		if p.Fatal {
			prefix = "ERROR"
			fatal = true
		}
		if p.File != "" {
			log.Printf("profile %s: %s: %s", prefix, p.File, p.Message)
		} else {
			log.Printf("profile %s: %s", prefix, p.Message)
		}
	}
	if fatal {
		return nil, fmt.Errorf("profile configuration invalid (see log); run 'wtmcpctl profile check' for details")
	}

	resolver, err := profile.NewResolver(cfg.Profiles.Default, loaded)
	if err != nil {
		return nil, fmt.Errorf("build profile resolver: %w", err)
	}

	if resolver.Configured() {
		log.Printf("profiles enabled: %d definitions loaded from %s", len(loaded.Definitions), profilesDir)
	}
	return resolver, nil
}

// profileTransportOptions builds the transport options that inject the
// per-connection profile filter, and enforces the client_auth
// requirement for the streamable-http transport.
//
// profileFlag is the stdio --profile value (ignored for HTTP).
func profileTransportOptions(cfg *config.Config, resolver *profile.Resolver, profileFlag string) ([]transport.Option, error) {
	if !resolver.Configured() {
		if profileFlag != "" {
			log.Printf("WARNING: --profile %q ignored: no profiles configured", profileFlag)
		}
		return nil, nil
	}

	if cfg.Server.Transport == config.TransportStreamableHTTP {
		// Profiles identify agents by their verified client certificate.
		// Anything weaker than require would let a client connect with no
		// cert and fall through to the default profile.
		if cfg.Server.TLS == nil || cfg.Server.TLS.ClientAuth != config.ClientAuthRequire {
			return nil, fmt.Errorf(
				"profiles are configured with the %s transport, so server.tls.client_auth must be %q",
				config.TransportStreamableHTTP, config.ClientAuthRequire)
		}
		return []transport.Option{
			transport.WithHTTPContextFunc(httpProfileContextFunc(resolver)),
		}, nil
	}

	// stdio: identity comes from the --profile flag, resolved once.
	if profileFlag == "" {
		log.Printf("profiles configured but no --profile given on stdio transport; all tools visible")
		return nil, nil
	}
	filter, ok := resolver.FilterByName(profileFlag)
	if !ok {
		return nil, fmt.Errorf("--profile %q is not a defined profile", profileFlag)
	}
	log.Printf("stdio profile: %s", profileFlag)
	return []transport.Option{
		transport.WithStdioContextFunc(func(ctx context.Context) context.Context {
			return profile.WithFilter(ctx, filter)
		}),
	}, nil
}

// httpProfileContextFunc extracts the verified client identity from the
// TLS peer certificate, resolves it to a filter, and stores the filter
// in the request context for the tool filter to consume.
func httpProfileContextFunc(resolver *profile.Resolver) func(context.Context, *http.Request) context.Context {
	return func(ctx context.Context, r *http.Request) context.Context {
		var id profile.Identity
		if r.TLS != nil && len(r.TLS.PeerCertificates) > 0 {
			id = profile.ExtractIdentity(r.TLS.PeerCertificates[0])
		}
		filter := resolver.FilterFor(id)
		//nolint:gosec // G706: CN comes from a CA-verified client cert and is logged quoted
		log.Printf("profile: CN=%q -> %s", id.CN, profile.FilterName(filter))
		return profile.WithFilter(ctx, filter)
	}
}
