package main

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/LeGambiArt/wtmcp/pkg/handler"
	gogitlab "gitlab.com/gitlab-org/api/client-go"
)

// instance holds a named GitLab client.
type instance struct {
	Name          string
	URL           string
	Client        *gogitlab.Client
	Authenticated bool
}

// instances maps instance names to their clients.
var instances map[string]*instance

// defaultInstance is used when only one instance is configured
// or when no instance param is provided.
var defaultInstance string

// discoverInstances sets up GitLab clients using the core's HTTP
// proxy for auth and transport. Supports both single-instance
// (GITLAB_TOKEN + GITLAB_URL) and multi-instance
// (GITLAB_{NAME}_TOKEN + GITLAB_{NAME}_URL).
//
// For multi-instance, per-domain auth bindings are registered so
// the core proxy injects the correct PRIVATE-TOKEN per domain.
// The go-gitlab SDK sends an empty Private-Token header which the
// proxy strips before re-injecting the real token.
func discoverInstances(p *handler.Plugin) error {
	instances = make(map[string]*instance)
	defaultInstance = ""

	transport := handler.NewProxyTransport(p)

	multi := scanMultiInstance()

	if len(multi) > 0 {
		return setupMultiInstance(p, transport.Client(), multi)
	}
	return setupSingleInstance(p, transport.Client())
}

// multiInstanceEntry holds a discovered multi-instance configuration.
type multiInstanceEntry struct {
	Name     string
	URL      string
	TokenVar string
}

// scanMultiInstance scans the environment for GITLAB_{NAME}_TOKEN
// patterns that indicate multi-instance configuration.
func scanMultiInstance() []multiInstanceEntry {
	var entries []multiInstanceEntry
	for _, env := range os.Environ() {
		key, value, ok := strings.Cut(env, "=")
		if !ok || value == "" {
			continue
		}
		if !strings.HasPrefix(key, "GITLAB_") || !strings.HasSuffix(key, "_TOKEN") {
			continue
		}
		if key == "GITLAB_TOKEN" || key == "GITLAB_SSL_VERIFY" {
			continue
		}

		name := strings.ToLower(key[7 : len(key)-6])
		if name == "" {
			continue
		}

		urlKey := fmt.Sprintf("GITLAB_%s_URL", strings.ToUpper(name))
		gitlabURL := os.Getenv(urlKey)
		if gitlabURL == "" {
			gitlabURL = "https://gitlab.com"
		}

		entries = append(entries, multiInstanceEntry{
			Name:     name,
			URL:      gitlabURL,
			TokenVar: key,
		})
	}
	return entries
}

func setupMultiInstance(p *handler.Plugin, httpClient *http.Client, entries []multiInstanceEntry) error {
	var domains []string
	authBindings := make(map[string]string, len(entries))

	for _, e := range entries {
		client, err := gogitlab.NewClient("",
			gogitlab.WithBaseURL(e.URL),
			gogitlab.WithHTTPClient(httpClient),
		)
		if err != nil {
			return fmt.Errorf("instance %s: %w", e.Name, err)
		}
		instances[e.Name] = &instance{Name: e.Name, URL: e.URL, Client: client, Authenticated: true}

		if host := extractHost(e.URL); host != "" {
			domains = append(domains, host)
			authBindings[host] = e.TokenVar
		}
	}

	if len(instances) == 1 {
		for name := range instances {
			defaultInstance = name
		}
	}

	p.SetInitDomains(domains)
	p.SetAuthBindings(authBindings)

	return nil
}

// setupSingleInstance uses the static services.auth token from
// plugin.yaml — no auth bindings needed (single token, single domain).
func setupSingleInstance(p *handler.Plugin, httpClient *http.Client) error {
	gitlabURL := os.Getenv("GITLAB_URL")
	if gitlabURL == "" {
		gitlabURL = "https://gitlab.com"
	}

	client, err := gogitlab.NewClient("",
		gogitlab.WithBaseURL(gitlabURL),
		gogitlab.WithHTTPClient(httpClient),
	)
	if err != nil {
		return fmt.Errorf("gitlab client: %w", err)
	}

	instances = map[string]*instance{
		"default": {
			Name:          "default",
			URL:           gitlabURL,
			Client:        client,
			Authenticated: os.Getenv("GITLAB_TOKEN") != "",
		},
	}
	defaultInstance = "default"

	if host := extractHost(gitlabURL); host != "" {
		p.SetInitDomains([]string{host})
	}

	return nil
}

// resolveInstance returns the client for the given instance name.
// If name is empty, returns the default (only works with single instance).
func resolveInstance(name string) (*gogitlab.Client, error) {
	inst, err := resolveInstanceConfig(name)
	if err != nil {
		return nil, err
	}
	return inst.Client, nil
}

func resolveAuthenticatedInstance(name string) (*gogitlab.Client, error) {
	inst, err := resolveInstanceConfig(name)
	if err != nil {
		return nil, err
	}
	if !inst.Authenticated {
		return nil, fmt.Errorf("authentication required for this GitLab operation; configure a token for instance %q", inst.Name)
	}
	return inst.Client, nil
}

func resolveInstanceConfig(name string) (*instance, error) {
	if name == "" {
		if defaultInstance == "" {
			names := make([]string, 0, len(instances))
			for n := range instances {
				names = append(names, n)
			}
			return nil, fmt.Errorf("instance is required (available: %s)", strings.Join(names, ", "))
		}
		name = defaultInstance
	}

	inst, ok := instances[name]
	if !ok {
		names := make([]string, 0, len(instances))
		for n := range instances {
			names = append(names, n)
		}
		return nil, fmt.Errorf("unknown instance %q (available: %s)", name, strings.Join(names, ", "))
	}
	return inst, nil
}

// extractHost returns the hostname from a URL string.
func extractHost(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
