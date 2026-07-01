package auth

import (
	"fmt"
	"net/http"
	"slices"
)

// KnownProviderTypes lists the built-in auth provider type names.
// This is the single source of truth shared by the server and CLI.
var KnownProviderTypes = []string{
	"bearer", "basic", "kerberos/spnego", "oauth2", "refresh_token", "github_app",
}

// IsKnownProviderType reports whether name is a recognized provider type.
func IsKnownProviderType(name string) bool {
	return slices.Contains(KnownProviderTypes, name)
}

// NormalizeProviderType maps aliases to canonical names.
// Currently normalizes "kerberos" to "kerberos/spnego".
func NormalizeProviderType(name string) string {
	if name == "kerberos" {
		return "kerberos/spnego"
	}
	return name
}

// VariantConfig describes an auth configuration with optional variants.
type VariantConfig struct {
	// Type is the auth provider type (e.g., "bearer", "basic").
	// Used when there are no variants.
	Type string

	// Select chooses a variant: explicit name or "auto".
	Select string

	// VariantOrder preserves declaration order for auto-detection.
	VariantOrder []string

	// Variants maps variant names to their auth configs.
	Variants map[string]SingleAuthConfig
}

// SingleAuthConfig is the config for a single auth provider instance.
type SingleAuthConfig struct {
	Type            string
	Token           string
	Header          string
	Prefix          string
	Username        string
	Password        string
	SPN             string
	Scopes          []string
	CredentialsFile string
	TokenFile       string
	CredentialsDir  string
	TokenURL        string
	ClientID        string
	AppID           string
	InstallationID  string
	PrivateKey      string
	PrivateKeyFile  string
	BaseURL         string
	Transport       http.RoundTripper // safe transport injected by plugin manager
}

// ResolveVariant selects the appropriate auth provider from a variant config.
//
// If no variants are defined, creates a provider from the single Type.
// If Select is not "auto", uses the explicitly named variant.
// If Select is "auto", tries each variant in order and uses the first
// one with valid credentials.
func ResolveVariant(cfg VariantConfig) (Provider, error) {
	if len(cfg.Variants) == 0 {
		return providerFromConfig(cfg.Type, SingleAuthConfig{
			Type:     cfg.Type,
			Token:    "", // filled by caller from resolved config
			Username: "",
			Password: "",
		})
	}

	if cfg.Select != "auto" && cfg.Select != "" {
		variant, ok := cfg.Variants[cfg.Select]
		if !ok {
			return nil, fmt.Errorf("unknown auth variant: %s", cfg.Select)
		}
		return providerFromConfig(variant.Type, variant)
	}

	// Auto: try each variant in declaration order
	for _, name := range cfg.VariantOrder {
		variant := cfg.Variants[name]
		p, err := providerFromConfig(variant.Type, variant)
		if err != nil {
			continue
		}
		if p.Available() {
			return p, nil
		}
	}

	return nil, fmt.Errorf("no auth variant has valid credentials")
}

func providerFromConfig(typeName string, cfg SingleAuthConfig) (Provider, error) {
	switch typeName {
	case "bearer":
		return NewBearerProvider(cfg.Token, cfg.Header, cfg.Prefix)
	case "basic":
		return NewBasicProvider(cfg.Username, cfg.Password), nil
	case "kerberos/spnego":
		return NewKerberosProvider(cfg.SPN), nil
	case "oauth2":
		return NewOAuth2Provider(cfg.TokenFile, cfg.CredentialsFile, cfg.Scopes, cfg.CredentialsDir, cfg.Transport)
	case "refresh_token":
		return NewRefreshTokenProvider(cfg.TokenURL, cfg.ClientID, cfg.Token, cfg.Transport, cfg.TokenFile)
	default:
		return nil, fmt.Errorf("unknown auth type: %s", typeName)
	}
}
