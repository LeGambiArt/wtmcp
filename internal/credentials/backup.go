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
	"runtime"
	"time"

	"golang.org/x/crypto/argon2"
)

const maxBackupFileSize = 10 << 20 // 10 MB

// Argon2id parameter defaults.
const (
	argon2Memory      = 65536 // 64 MB in KB
	argon2Iterations  = 3
	argon2Parallelism = 4
	argon2SaltLen     = 16
	argon2KeyLen      = 32 // AES-256
)

// BackupFile is the JSON structure written to disk for encrypted
// credential backups. It contains all the information needed to
// derive the decryption key and decrypt the payload.
type BackupFile struct {
	Version    int          `json:"version"`
	CreatedAt  string       `json:"created_at"` // RFC3339
	KDF        string       `json:"kdf"`        // "argon2id"
	KDFParams  Argon2Params `json:"kdf_params"`
	IV         string       `json:"iv"`         // base64
	Ciphertext string       `json:"ciphertext"` // base64
	Tag        string       `json:"tag"`        // base64
}

// Argon2Params contains the Argon2id parameters used for key
// derivation. All parameters are stored in the backup file so the
// key can be re-derived during restore.
type Argon2Params struct {
	Memory      uint32 `json:"memory"`      // in KB
	Iterations  uint32 `json:"iterations"`  // time parameter
	Parallelism uint8  `json:"parallelism"` // threads
	Salt        string `json:"salt"`        // base64
}

// BackupPayload is the plaintext structure that gets encrypted and
// stored inside the backup file. It contains all credential groups
// and the list of migrated groups.
type BackupPayload struct {
	CredentialGroups map[string]map[string]string `json:"credential_groups"`
	MigrationState   []string                     `json:"migration_state"`
}

// Sentinel errors for backup operations.
var (
	// ErrInvalidPassword is returned when the backup password is
	// incorrect (GCM authentication failure).
	ErrInvalidPassword = errors.New("invalid password or corrupted backup")

	// ErrUnsupportedVersion is returned when the backup file
	// version is not supported.
	ErrUnsupportedVersion = errors.New("unsupported backup version")

	// ErrEmptyPassword is returned when an empty password is
	// provided for backup or restore.
	ErrEmptyPassword = errors.New("password must not be empty")
)

// CreateBackup exports all migrated credentials to an encrypted
// backup file. The backup is encrypted using AES-256-GCM with a key
// derived from the password using Argon2id.
//
// If outputPath is empty, a default path is used:
// ~/.config/wtmcp/credentials/backup-<timestamp>.enc
//
// The password slice is NOT zeroed by CreateBackup; callers are
// responsible for zeroing it after the call returns.
func (s *Service) CreateBackup(password []byte, outputPath string) error {
	if len(password) == 0 {
		return ErrEmptyPassword
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	// Build the backup payload.
	payload, err := s.buildBackupPayload()
	if err != nil {
		return fmt.Errorf("build backup payload: %w", err)
	}

	// Derive the encryption key from the password.
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return fmt.Errorf("generate salt: %w", err)
	}

	key := deriveKey(password, salt, argon2Memory, argon2Iterations, argon2Parallelism)
	defer zeroBackupKey(key)

	// Encrypt the payload.
	backup, err := encryptBackupPayload(key, salt, payload)
	if err != nil {
		return fmt.Errorf("encrypt backup: %w", err)
	}

	// Determine output path.
	if outputPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("get home directory: %w", err)
		}
		credDir := filepath.Join(homeDir, ".config", "wtmcp", "credentials")
		if err := os.MkdirAll(credDir, 0o700); err != nil {
			return fmt.Errorf("create credentials directory: %w", err)
		}
		timestamp := time.Now().UTC().Format("20060102-150405")
		outputPath = filepath.Join(credDir, fmt.Sprintf("backup-%s.enc", timestamp))
	}

	// Marshal to JSON.
	data, err := json.MarshalIndent(backup, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal backup: %w", err)
	}

	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}

	// Write the backup file with restricted permissions.
	if err := os.WriteFile(outputPath, data, 0o600); err != nil {
		return fmt.Errorf("write backup file: %w", err)
	}

	return nil
}

// RestoreBackup imports credentials from an encrypted backup file.
// If mergeMode is true, only keys that do not already exist in the
// keyring are imported. If false, all keys are imported and existing
// values are overwritten.
//
// The password slice is NOT zeroed by RestoreBackup; callers are
// responsible for zeroing it after the call returns.
func (s *Service) RestoreBackup(backupPath string, password []byte, mergeMode bool) error {
	if len(password) == 0 {
		return ErrEmptyPassword
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Guard against oversized files before reading into memory.
	info, err := os.Stat(backupPath)
	if err != nil {
		return fmt.Errorf("stat backup file: %w", err)
	}
	if info.Size() > maxBackupFileSize {
		return fmt.Errorf("backup file too large: %d bytes (max %d)", info.Size(), maxBackupFileSize)
	}

	// Read the backup file.
	data, err := os.ReadFile(backupPath) //nolint:gosec // user-provided path
	if err != nil {
		return fmt.Errorf("read backup file: %w", err)
	}

	// Parse the backup file.
	var backup BackupFile
	if err := json.Unmarshal(data, &backup); err != nil {
		return fmt.Errorf("parse backup file: %w", err)
	}

	// Validate version.
	if backup.Version != 1 {
		return fmt.Errorf("%w: %d", ErrUnsupportedVersion, backup.Version)
	}

	// Decode the salt.
	salt, err := base64.StdEncoding.DecodeString(backup.KDFParams.Salt)
	if err != nil {
		return fmt.Errorf("decode salt: %w", err)
	}

	// Validate KDF parameters to prevent a crafted backup from
	// causing OOM or excessive CPU usage.
	if err := validateKDFParams(backup.KDFParams); err != nil {
		return fmt.Errorf("unsafe KDF parameters: %w", err)
	}

	// Derive the encryption key using params from the backup.
	key := deriveKey(
		password,
		salt,
		backup.KDFParams.Memory,
		backup.KDFParams.Iterations,
		backup.KDFParams.Parallelism,
	)
	defer zeroBackupKey(key)

	// Decrypt the payload.
	payload, err := decryptBackupPayload(key, &backup)
	if err != nil {
		return fmt.Errorf("decrypt backup: %w", err)
	}

	// Import credentials. Track written keys so we can roll back on failure.
	type restoredKey struct{ group, key string }
	var restored []restoredKey
	for group, keys := range payload.CredentialGroups {
		for k, v := range keys {
			if mergeMode {
				if _, existErr := s.keyringStore.Get(group, k); existErr == nil {
					if err := s.migrationState.TrackKey(group, k); err != nil {
						return fmt.Errorf("track key %s/%s: %w", group, k, err)
					}
					continue
				}
			}
			if err := s.keyringStore.Set(group, k, v); err != nil {
				for _, rk := range restored {
					if derr := s.keyringStore.Delete(rk.group, rk.key); derr != nil {
						log.Printf("[credentials] warning: rollback failed for %s/%s: %v", rk.group, rk.key, derr)
					}
				}
				return fmt.Errorf("restore %s/%s (rolled back %d keys): %w", group, k, len(restored), err)
			}
			restored = append(restored, restoredKey{group, k})
			if err := s.migrationState.TrackKey(group, k); err != nil {
				return fmt.Errorf("track key %s/%s: %w", group, k, err)
			}
		}
	}

	// Update migration state with groups from backup.
	for _, group := range payload.MigrationState {
		if err := s.migrationState.MarkMigrated(group); err != nil {
			return fmt.Errorf("mark migrated %s: %w", group, err)
		}
	}

	// Clear the cache so subsequent reads pick up restored values.
	s.cache.Clear()

	return nil
}

// buildBackupPayload constructs the backup payload from current
// migration state and keyring contents. It uses the tracked keys
// in migration state to enumerate keyring contents.
func (s *Service) buildBackupPayload() (*BackupPayload, error) {
	groups := s.migrationState.GetMigratedGroups()
	payload := &BackupPayload{
		CredentialGroups: make(map[string]map[string]string),
		MigrationState:   groups,
	}

	for _, group := range groups {
		keys := s.migrationState.GetGroupKeys(group)
		if len(keys) == 0 {
			// No tracked keys for this group; still record it
			// with an empty map so the group appears in the backup.
			payload.CredentialGroups[group] = make(map[string]string)
			continue
		}

		groupCreds := make(map[string]string)
		for _, key := range keys {
			value, err := s.keyringStore.Get(group, key)
			if err != nil {
				if errors.Is(err, ErrCredentialNotFound) {
					// Key was tracked but no longer in keyring; skip.
					continue
				}
				return nil, fmt.Errorf("get %s/%s from keyring: %w", group, key, err)
			}
			groupCreds[key] = value
		}
		payload.CredentialGroups[group] = groupCreds
	}

	return payload, nil
}

const (
	maxKDFMemory      = 256 * 1024 // 256 MB in KB
	maxKDFIterations  = 20
	maxKDFParallelism = 16
)

func validateKDFParams(p Argon2Params) error {
	if p.Memory == 0 || p.Memory > maxKDFMemory {
		return fmt.Errorf("memory %d KB out of range (1 – %d KB)", p.Memory, maxKDFMemory)
	}
	if p.Iterations == 0 || p.Iterations > maxKDFIterations {
		return fmt.Errorf("iterations %d out of range (1 – %d)", p.Iterations, maxKDFIterations)
	}
	if p.Parallelism == 0 || p.Parallelism > maxKDFParallelism {
		return fmt.Errorf("parallelism %d out of range (1 – %d)", p.Parallelism, maxKDFParallelism)
	}
	return nil
}

// deriveKey derives an encryption key from a password using Argon2id.
func deriveKey(password, salt []byte, memory, iterations uint32, parallelism uint8) []byte {
	return argon2.IDKey(password, salt, iterations, memory, parallelism, argon2KeyLen)
}

func zeroBackupKey(key []byte) {
	for i := range key {
		key[i] = 0
	}
	runtime.KeepAlive(key)
}

// encryptBackupPayload encrypts a BackupPayload using AES-256-GCM
// and returns a BackupFile ready for serialisation.
func encryptBackupPayload(key, salt []byte, payload *BackupPayload) (*BackupFile, error) {
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
	plaintext, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}
	defer zeroBackupKey(plaintext)

	// Encrypt with GCM. AAD binds the backup metadata (version, KDF
	// type, and parameters) to the ciphertext so that tampering with
	// the envelope is detected by GCM authentication.
	aad := []byte(fmt.Sprintf("wtmcp-backup-v%d;%s;m%d;t%d;p%d",
		1, "argon2id", argon2Memory, argon2Iterations, argon2Parallelism))
	sealed := gcm.Seal(nil, nonce, plaintext, aad)

	// Split sealed into ciphertext and tag.
	overhead := gcm.Overhead()
	ciphertext := sealed[:len(sealed)-overhead]
	tag := sealed[len(sealed)-overhead:]

	return &BackupFile{
		Version:   1,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		KDF:       "argon2id",
		KDFParams: Argon2Params{
			Memory:      argon2Memory,
			Iterations:  argon2Iterations,
			Parallelism: argon2Parallelism,
			Salt:        base64.StdEncoding.EncodeToString(salt),
		},
		IV:         base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
		Tag:        base64.StdEncoding.EncodeToString(tag),
	}, nil
}

// decryptBackupPayload decrypts a BackupFile to a BackupPayload.
// Returns ErrInvalidPassword if the GCM authentication fails.
func decryptBackupPayload(key []byte, backup *BackupFile) (*BackupPayload, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	// Decode base64 fields.
	nonce, err := base64.StdEncoding.DecodeString(backup.IV)
	if err != nil {
		return nil, fmt.Errorf("decode IV: %w", err)
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("invalid IV length %d (expected %d)", len(nonce), gcm.NonceSize())
	}

	ciphertext, err := base64.StdEncoding.DecodeString(backup.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}

	tag, err := base64.StdEncoding.DecodeString(backup.Tag)
	if err != nil {
		return nil, fmt.Errorf("decode tag: %w", err)
	}

	// Combine ciphertext + tag for GCM Open.
	sealed := make([]byte, 0, len(ciphertext)+len(tag))
	sealed = append(sealed, ciphertext...)
	sealed = append(sealed, tag...)

	// Decrypt with AAD that binds the backup metadata.
	aad := []byte(fmt.Sprintf("wtmcp-backup-v%d;%s;m%d;t%d;p%d",
		backup.Version, backup.KDF,
		backup.KDFParams.Memory, backup.KDFParams.Iterations, backup.KDFParams.Parallelism))
	plaintext, err := gcm.Open(nil, nonce, sealed, aad)
	if err != nil {
		return nil, ErrInvalidPassword
	}
	defer zeroBackupKey(plaintext)

	// Unmarshal the payload.
	var payload BackupPayload
	if err := json.Unmarshal(plaintext, &payload); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	return &payload, nil
}
