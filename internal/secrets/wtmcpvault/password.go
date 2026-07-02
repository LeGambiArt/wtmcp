package wtmcpvault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters matching OWASP recommendations (2023).
const (
	argon2Memory      = 65536 // 64 MB in KB
	argon2Iterations  = 3
	argon2Parallelism = 4
	argon2SaltLen     = 16
	argon2KeyLen      = 32 // AES-256

	gcmNonceLen = 12

	// payloadVersion is embedded in the binary payload for future
	// format evolution.
	payloadVersion byte = 1

	// Minimum payload: version(1) + salt(16) + nonce(12) + tag(16)
	minPayloadLen = 1 + argon2SaltLen + gcmNonceLen + 16
)

// EncryptPassword encrypts plaintext with the PASSWORD backend.
// The password is used to derive an AES-256 key via Argon2id.
// AAD (additional authenticated data) binds the ciphertext to its
// context (e.g., header + credential group name) to prevent
// file-swap attacks.
//
// Output format: header line + newline + binary payload.
// Payload: [version(1)][salt(16)][nonce(12)][ciphertext+tag].
func EncryptPassword(plaintext, password, aad []byte) ([]byte, error) {
	if len(password) == 0 {
		return nil, fmt.Errorf("password must not be empty")
	}

	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey(password, salt, argon2Iterations, argon2Memory, argon2Parallelism, argon2KeyLen)
	defer zeroBytes(key)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	sealed := gcm.Seal(nil, nonce, plaintext, aad)

	header := FormatHeader(BackendPassword)

	// Binary payload: version + salt + nonce + sealed(ciphertext+tag)
	payloadLen := 1 + argon2SaltLen + gcmNonceLen + len(sealed)
	payload := make([]byte, 0, payloadLen)
	payload = append(payload, payloadVersion)
	payload = append(payload, salt...)
	payload = append(payload, nonce...)
	payload = append(payload, sealed...)

	result := make([]byte, 0, len(header)+1+len(payload))
	result = append(result, []byte(header)...)
	result = append(result, '\n')
	result = append(result, payload...)

	return result, nil
}

// DecryptPassword decrypts data encrypted with the PASSWORD backend.
// The AAD must match what was used during encryption.
func DecryptPassword(data, password, aad []byte) ([]byte, error) {
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", len(data), maxFileSize)
	}

	headerLine, payload, err := splitHeaderPayload(data)
	if err != nil {
		return nil, err
	}

	header, err := ParseHeader(headerLine)
	if err != nil {
		return nil, err
	}
	if header.Backend != BackendPassword {
		return nil, fmt.Errorf("expected PASSWORD backend, got %s", header.Backend)
	}

	if len(payload) < minPayloadLen {
		return nil, fmt.Errorf("payload too short: %d bytes", len(payload))
	}

	if payload[0] != payloadVersion {
		return nil, fmt.Errorf("unsupported payload version: %d", payload[0])
	}

	salt := payload[1 : 1+argon2SaltLen]
	nonce := payload[1+argon2SaltLen : 1+argon2SaltLen+gcmNonceLen]
	sealed := payload[1+argon2SaltLen+gcmNonceLen:]

	key := argon2.IDKey(password, salt, argon2Iterations, argon2Memory, argon2Parallelism, argon2KeyLen)
	defer zeroBytes(key)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	plaintext, err := gcm.Open(nil, nonce, sealed, aad)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (wrong password or tampered data)")
	}

	return plaintext, nil
}

// splitHeaderPayload splits $WTMCP_VAULT data into header line and
// binary payload at the first newline.
func splitHeaderPayload(data []byte) (string, []byte, error) {
	idx := -1
	for i, b := range data {
		if b == '\n' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", nil, fmt.Errorf("invalid wtmcp vault file: missing newline after header")
	}
	return string(data[:idx]), data[idx+1:], nil
}

// BuildAAD constructs the additional authenticated data from the
// header line and a context string (typically the credential group
// name). This binds the ciphertext to its intended context and
// prevents file-swap attacks.
func BuildAAD(header, context string) []byte {
	aad := make([]byte, 0, len(header)+1+len(context))
	aad = append(aad, []byte(header)...)
	aad = append(aad, 0) // null separator
	aad = append(aad, []byte(context)...)
	return aad
}

// EncryptPasswordWithContext is a convenience wrapper that encrypts
// plaintext with PASSWORD backend, building AAD from the credential
// group name.
func EncryptPasswordWithContext(plaintext, password []byte, credentialGroup string) ([]byte, error) {
	header := FormatHeader(BackendPassword)
	aad := BuildAAD(header, credentialGroup)
	return EncryptPassword(plaintext, password, aad)
}

// DecryptPasswordWithContext is a convenience wrapper that decrypts
// data encrypted with PASSWORD backend, building AAD from the
// credential group name.
func DecryptPasswordWithContext(data, password []byte, credentialGroup string) ([]byte, error) {
	headerLine, _, err := splitHeaderPayload(data)
	if err != nil {
		return nil, err
	}
	aad := BuildAAD(headerLine, credentialGroup)
	return DecryptPassword(data, password, aad)
}

// Argon2Params returns the Argon2id parameters used for key derivation.
// Exported for diagnostic output and interop with backup.go.
func Argon2Params() (memory uint32, iterations uint32, parallelism uint8) {
	return argon2Memory, argon2Iterations, argon2Parallelism
}

// ExtractHeader extracts the header line from $WTMCP_VAULT data
// without parsing the full payload.
func ExtractHeader(data []byte) (string, error) {
	h, _, err := splitHeaderPayload(data)
	return h, err
}

// ExtractSalt returns the Argon2 salt from a PASSWORD-encrypted payload
// for diagnostic purposes. Does not decrypt.
func ExtractSalt(data []byte) ([]byte, error) {
	_, payload, err := splitHeaderPayload(data)
	if err != nil {
		return nil, err
	}
	if len(payload) < 1+argon2SaltLen {
		return nil, fmt.Errorf("payload too short for salt extraction")
	}
	if payload[0] != payloadVersion {
		return nil, fmt.Errorf("unsupported payload version: %d", payload[0])
	}
	salt := make([]byte, argon2SaltLen)
	copy(salt, payload[1:1+argon2SaltLen])
	return salt, nil
}

// PayloadSize returns the header and ciphertext overhead in bytes
// added to plaintext of the given length.
func PayloadSize(plaintextLen int) int {
	headerLen := len(FormatHeader(BackendPassword))
	tagSize := 16
	return headerLen + 1 + 1 + argon2SaltLen + gcmNonceLen + plaintextLen + tagSize
}

// FormatInfo returns a human-readable description of a $WTMCP_VAULT
// PASSWORD file for diagnostic output.
func FormatInfo(data []byte) string {
	headerLine, payload, err := splitHeaderPayload(data)
	if err != nil {
		return "wtmcp vault (invalid)"
	}
	header, err := ParseHeader(headerLine)
	if err != nil {
		return "wtmcp vault (invalid header)"
	}

	info := fmt.Sprintf("wtmcp v%d %s", header.Version, strings.ToLower(header.Backend))
	if header.Backend == BackendPassword && len(payload) >= 1+argon2SaltLen {
		mem, iter, par := Argon2Params()
		info += fmt.Sprintf(" argon2id(%dMB/%d/%d)", mem/1024, iter, par)
	}
	return info
}

// SaltSize returns the size of Argon2 salt used in PASSWORD backend.
func SaltSize() int {
	return argon2SaltLen
}

// EncryptedPayloadOverhead returns the number of bytes added to
// plaintext by the binary payload format (version + salt + nonce +
// GCM tag), excluding the header line.
func EncryptedPayloadOverhead() int {
	return 1 + argon2SaltLen + gcmNonceLen + 16
}

// KeySize returns the encryption key length in bytes for the binary
// payload. Currently reads the constant so callers do not depend on
// the module-level value.
func KeySize() int {
	return argon2KeyLen
}

// EncodeLenU16 encodes a length into a 2-byte little-endian field.
func EncodeLenU16(n int) []byte {
	b := make([]byte, 2)
	binary.LittleEndian.PutUint16(b, uint16(n)) //nolint:gosec // n is bounded by callers
	return b
}

func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
	runtime.KeepAlive(b)
}
