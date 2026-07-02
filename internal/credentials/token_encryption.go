package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/oauth2"
)

// TokenEncryption provides AES-256-GCM encryption for OAuth token
// files. Access tokens are stored directly in the OS keyring, while
// refresh tokens and metadata are encrypted on disk.
type TokenEncryption struct {
	keyringStore   *KeyringStore
	migrationState *MigrationState
	tokensDir      string
	mu             sync.Mutex
}

// EncryptedTokenFile is the JSON structure written to disk for
// encrypted token files. It contains the AES-256-GCM nonce (IV),
// ciphertext, and authentication tag, all base64-encoded.
type EncryptedTokenFile struct {
	Version    int    `json:"version"`
	IV         string `json:"iv"`
	Ciphertext string `json:"ciphertext"`
	Tag        string `json:"tag"`
}

// TokenPayload is the plaintext structure that gets encrypted and
// stored on disk. It contains the refresh token and token metadata
// but not the access token (which goes into the keyring).
type TokenPayload struct {
	RefreshToken string   `json:"refresh_token"`
	Expiry       string   `json:"expiry"`
	TokenType    string   `json:"token_type"`
	Scopes       []string `json:"scopes,omitempty"`
}

// NewTokenEncryption creates a new TokenEncryption that uses the
// given keyring store and tokens directory.
func NewTokenEncryption(keyringStore *KeyringStore, migrationState *MigrationState, tokensDir string) *TokenEncryption {
	return &TokenEncryption{
		keyringStore:   keyringStore,
		migrationState: migrationState,
		tokensDir:      tokensDir,
	}
}

// SaveToken encrypts and saves an OAuth token. The refresh token and metadata
// are AES-256-GCM encrypted on disk first; only after a successful disk write
// is the access token stored in the keyring. This ordering ensures that a disk
// write failure never leaves a mismatched keyring/disk state.
func (te *TokenEncryption) SaveToken(group, pluginName string, token *oauth2.Token) error {
	// Get or create the group encryption key.
	key, err := te.getOrCreateEncryptionKey(group)
	if err != nil {
		return fmt.Errorf("get encryption key: %w", err)
	}

	// Build the payload for encryption.
	payload := TokenPayload{
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
	}
	if !token.Expiry.IsZero() {
		payload.Expiry = token.Expiry.Format(time.RFC3339)
	}

	// Encrypt the payload.
	encrypted, err := te.encryptPayload(key, group, pluginName, payload)
	if err != nil {
		return fmt.Errorf("encrypt payload: %w", err)
	}

	// Marshal to JSON.
	data, err := json.MarshalIndent(encrypted, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal encrypted token: %w", err)
	}

	// Ensure the group directory exists.
	groupDir := filepath.Join(te.tokensDir, group)
	if err := os.MkdirAll(groupDir, 0o700); err != nil {
		return fmt.Errorf("create token directory: %w", err)
	}

	// Write the encrypted file to disk atomically via temp+rename so a
	// crash mid-write cannot leave a truncated or empty file behind.
	filePath := filepath.Join(groupDir, fmt.Sprintf("token-%s.json.enc", pluginName))
	tmpFile, err := os.CreateTemp(groupDir, ".token-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	if _, werr := tmpFile.Write(data); werr != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("write encrypted token: %w", werr)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("sync encrypted token: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close encrypted token: %w", err)
	}
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("chmod encrypted token: %w", err)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename encrypted token: %w", err)
	}

	// Store the access token in the keyring now that the disk write succeeded.
	accessKeyName := fmt.Sprintf("__oauth_access_%s", pluginName)
	if err := te.keyringStore.Set(group, accessKeyName, token.AccessToken); err != nil {
		// Disk has the new payload but keyring is stale; the token will be
		// refreshed on next load. Log but do not roll back the disk write.
		return fmt.Errorf("store access token: %w", err)
	}

	te.trackKey(group, accessKeyName)

	return nil
}

func (te *TokenEncryption) trackKey(group, key string) {
	if te.migrationState == nil {
		return
	}
	if err := te.migrationState.TrackKey(group, key); err != nil {
		log.Printf("[credentials] warning: failed to track key %s/%s: %v", group, key, err)
	}
}

// LoadToken loads and decrypts an OAuth token. The access token is
// retrieved from the keyring, while the refresh token and metadata
// are decrypted from disk.
func (te *TokenEncryption) LoadToken(group, pluginName string) (*oauth2.Token, error) {
	// Get the encryption key.
	key, err := te.getEncryptionKey(group)
	if err != nil {
		return nil, fmt.Errorf("get encryption key: %w", err)
	}

	// Load the encrypted file.
	filePath := filepath.Join(te.tokensDir, group, fmt.Sprintf("token-%s.json.enc", pluginName))
	data, err := os.ReadFile(filePath) //nolint:gosec // token file path
	if err != nil {
		return nil, fmt.Errorf("read encrypted token file: %w", err)
	}

	// Parse the encrypted file.
	var encrypted EncryptedTokenFile
	if err := json.Unmarshal(data, &encrypted); err != nil {
		return nil, fmt.Errorf("parse encrypted token file: %w", err)
	}

	// Decrypt the payload.
	var payload TokenPayload
	if err := te.decryptPayload(key, group, pluginName, &encrypted, &payload); err != nil {
		return nil, fmt.Errorf("decrypt payload: %w", err)
	}

	// Get the access token from the keyring. If missing (e.g. after a
	// backup restore), return a token with an empty AccessToken and a
	// zero Expiry so the OAuth refresh flow kicks in automatically.
	accessKeyName := fmt.Sprintf("__oauth_access_%s", pluginName)
	accessToken, err := te.keyringStore.Get(group, accessKeyName)
	if err != nil && !errors.Is(err, ErrCredentialNotFound) {
		return nil, fmt.Errorf("get access token: %w", err)
	}

	// Reconstruct the oauth2.Token.
	token := &oauth2.Token{
		AccessToken:  accessToken,
		RefreshToken: payload.RefreshToken,
		TokenType:    payload.TokenType,
	}

	switch {
	case accessToken != "" && payload.Expiry != "":
		expiry, err := time.Parse(time.RFC3339, payload.Expiry)
		if err != nil {
			return nil, fmt.Errorf("parse expiry: %w", err)
		}
		token.Expiry = expiry
	case accessToken == "":
		// Access token is missing (e.g. after backup restore). Set expiry
		// in the past so oauth2.Token.Valid() returns false and the refresh
		// flow kicks in automatically.
		token.Expiry = time.Now().Add(-time.Minute)
	}

	return token, nil
}

// MigrateUnencryptedToken migrates a plaintext token file to
// encrypted format. If no plaintext file exists, this is a no-op.
func (te *TokenEncryption) MigrateUnencryptedToken(group, pluginName string) error {
	plaintextPath := filepath.Join(te.tokensDir, group, fmt.Sprintf("token-%s.json", pluginName))

	// Check if plaintext file exists.
	data, err := os.ReadFile(plaintextPath) //nolint:gosec // token file path
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Already migrated or never existed.
		}
		return fmt.Errorf("read plaintext token: %w", err)
	}

	// Parse the plaintext token.
	var token oauth2.Token
	if err := json.Unmarshal(data, &token); err != nil {
		return fmt.Errorf("parse plaintext token: %w", err)
	}

	// Save as encrypted.
	if err := te.SaveToken(group, pluginName, &token); err != nil {
		return fmt.Errorf("save encrypted token: %w", err)
	}

	// Remove the plaintext file.
	if err := os.Remove(plaintextPath); err != nil {
		return fmt.Errorf("remove plaintext token: %w", err)
	}

	log.Printf("[credentials] migrated plaintext token to encrypted: %s/%s", group, pluginName)
	return nil
}

// getOrCreateEncryptionKey retrieves or generates the AES-256
// encryption key for a credential group. Keys are stored
// base64-encoded in the keyring.
func (te *TokenEncryption) getOrCreateEncryptionKey(group string) ([]byte, error) {
	te.mu.Lock()
	defer te.mu.Unlock()

	value, err := te.keyringStore.Get(group, "__encryption_key")
	if err == nil {
		key, derr := base64.StdEncoding.DecodeString(value)
		if derr != nil {
			return nil, fmt.Errorf("decode encryption key: %w", derr)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("encryption key has wrong length %d (expected 32 bytes); the keyring entry may be corrupted", len(key))
		}
		return key, nil
	}

	if !errors.Is(err, ErrCredentialNotFound) {
		return nil, fmt.Errorf("keyring get encryption key: %w", err)
	}

	// Generate a new 256-bit key.
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}

	// Store base64-encoded in the keyring.
	encoded := base64.StdEncoding.EncodeToString(key)
	if err := te.keyringStore.Set(group, "__encryption_key", encoded); err != nil {
		return nil, fmt.Errorf("store encryption key: %w", err)
	}

	te.trackKey(group, "__encryption_key")

	return key, nil
}

// getEncryptionKey retrieves the AES-256 encryption key for a
// credential group. Returns an error if the key does not exist.
func (te *TokenEncryption) getEncryptionKey(group string) ([]byte, error) {
	value, err := te.keyringStore.Get(group, "__encryption_key")
	if err != nil {
		return nil, fmt.Errorf("keyring get encryption key: %w", err)
	}

	key, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode encryption key: %w", err)
	}

	if len(key) != 32 {
		return nil, fmt.Errorf("encryption key has wrong length %d (expected 32 bytes); the keyring entry may be corrupted", len(key))
	}

	return key, nil
}

// encryptPayload encrypts a TokenPayload using AES-256-GCM and
// returns an EncryptedTokenFile ready for serialisation to disk.
// The group and pluginName are bound as additional authenticated data
// (AAD) to prevent ciphertext from being swapped between plugins.
func (te *TokenEncryption) encryptPayload(key []byte, group, pluginName string, payload TokenPayload) (*EncryptedTokenFile, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	// Generate a random nonce.
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	// Marshal the payload to JSON.
	plaintext, err := json.Marshal(payload) //nolint:gosec // tokens are immediately encrypted with AES-256-GCM
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	// Encrypt with GCM. AAD binds the ciphertext to this specific
	// group/plugin so files cannot be swapped between plugins.
	aad := []byte(group + "\x00" + pluginName)
	sealed := gcm.Seal(nil, nonce, plaintext, aad)

	// Split sealed into ciphertext and tag.
	overhead := gcm.Overhead()
	ciphertext := sealed[:len(sealed)-overhead]
	tag := sealed[len(sealed)-overhead:]

	return &EncryptedTokenFile{
		Version:    1,
		IV:         base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
		Tag:        base64.StdEncoding.EncodeToString(tag),
	}, nil
}

// decryptPayload decrypts an EncryptedTokenFile using AES-256-GCM
// and unmarshals the result into the given payload. The group and
// pluginName must match the values used during encryption.
func (te *TokenEncryption) decryptPayload(key []byte, group, pluginName string, encrypted *EncryptedTokenFile, payload *TokenPayload) error {
	block, err := aes.NewCipher(key)
	if err != nil {
		return fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return fmt.Errorf("create GCM: %w", err)
	}

	// Decode base64 fields.
	nonce, err := base64.StdEncoding.DecodeString(encrypted.IV)
	if err != nil {
		return fmt.Errorf("decode IV: %w", err)
	}
	if len(nonce) != gcm.NonceSize() {
		return fmt.Errorf("invalid IV length %d (expected %d)", len(nonce), gcm.NonceSize())
	}

	ciphertext, err := base64.StdEncoding.DecodeString(encrypted.Ciphertext)
	if err != nil {
		return fmt.Errorf("decode ciphertext: %w", err)
	}

	tag, err := base64.StdEncoding.DecodeString(encrypted.Tag)
	if err != nil {
		return fmt.Errorf("decode tag: %w", err)
	}

	// Combine ciphertext + tag for GCM Open.
	sealed := make([]byte, 0, len(ciphertext)+len(tag))
	sealed = append(sealed, ciphertext...)
	sealed = append(sealed, tag...)

	// Decrypt with AAD to verify group/plugin binding.
	aad := []byte(group + "\x00" + pluginName)
	plaintext, err := gcm.Open(nil, nonce, sealed, aad)
	if err != nil {
		return fmt.Errorf("GCM decrypt: %w", err)
	}

	// Unmarshal into payload.
	if err := json.Unmarshal(plaintext, payload); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}

	return nil
}
