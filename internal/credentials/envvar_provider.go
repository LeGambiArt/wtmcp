package credentials

import "os"

// EnvVarProvider reads credentials from the process environment.
// The group parameter is ignored — credentials are resolved
// directly by key using os.Getenv.
type EnvVarProvider struct{}

// NewEnvVarProvider creates a new EnvVarProvider.
func NewEnvVarProvider() *EnvVarProvider {
	return &EnvVarProvider{}
}

// Get retrieves a credential from the process environment.
// The group parameter is ignored. Returns ErrCredentialNotFound
// if the environment variable is not set.
func (p *EnvVarProvider) Get(_ string, key string) (string, error) {
	value, ok := os.LookupEnv(key)
	if !ok {
		return "", ErrCredentialNotFound
	}
	return value, nil
}
