package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	sessionCookie        = "video_dashboard_session"
	sessionLifetime      = 24 * time.Hour
	recoveryCodeLifetime = 15 * time.Minute
	passwordIterations   = 210000
)

const recoveryAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

type Security struct {
	store *Store
	aead  cipher.AEAD
}

type SessionIdentity struct {
	Username           string
	MustChangePassword bool
}

func NewSecurity(store *Store) (*Security, error) {
	key, err := loadOrCreateSecret(filepath.Join(store.dataDir, "secret.key"))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Security{store: store, aead: aead}, nil
}

func loadOrCreateSecret(path string) ([]byte, error) {
	if data, err := os.ReadFile(path); err == nil {
		if len(data) != 32 {
			return nil, errors.New("invalid encryption key length")
		}
		return data, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return os.ReadFile(path)
	}
	if err != nil {
		return nil, err
	}
	if _, err = file.Write(key); err != nil {
		file.Close()
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	return key, nil
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	key := pbkdf2SHA256([]byte(password), salt, passwordIterations, 32)
	return fmt.Sprintf("pbkdf2_sha256$%d$%s$%s", passwordIterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func checkPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 100000 || iterations > 1000000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	actual := pbkdf2SHA256([]byte(password), salt, iterations, len(expected))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func pbkdf2SHA256(password, salt []byte, iterations, keyLength int) []byte {
	hashLength := sha256.Size
	blocks := (keyLength + hashLength - 1) / hashLength
	output := make([]byte, 0, blocks*hashLength)
	for block := 1; block <= blocks; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		result := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range result {
				result[j] ^= u[j]
			}
		}
		output = append(output, result...)
	}
	return output[:keyLength]
}

func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func generateRecoveryCode() (string, error) {
	random := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, random); err != nil {
		return "", err
	}
	code := make([]byte, 0, 19)
	for i, value := range random {
		if i > 0 && i%4 == 0 {
			code = append(code, '-')
		}
		code = append(code, recoveryAlphabet[int(value)%len(recoveryAlphabet)])
	}
	return string(code), nil
}

func normalizeRecoveryCode(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))
}

func recoveryCodeHash(code string) string {
	return tokenHash(normalizeRecoveryCode(code))
}

func (s *Security) NewSession(w http.ResponseWriter, r *http.Request, username string) error {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := time.Now().Add(sessionLifetime)
	if err := s.store.CreateSession(tokenHash(token), username, expires.Unix()); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: expires, MaxAge: int(sessionLifetime.Seconds()), HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"})
	return nil
}

func (s *Security) SessionUser(r *http.Request) (string, bool) {
	identity, ok := s.Session(r)
	return identity.Username, ok
}

func (s *Security) Session(r *http.Request) (SessionIdentity, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || len(cookie.Value) < 32 {
		return SessionIdentity{}, false
	}
	username, mustChange, err := s.store.SessionUser(tokenHash(cookie.Value), time.Now().Unix())
	return SessionIdentity{Username: username, MustChangePassword: mustChange}, err == nil
}

func (s *Security) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		s.store.DeleteSession(tokenHash(cookie.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteStrictMode})
}

func (s *Security) Encrypt(value string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	sealed := s.aead.Seal(nonce, nonce, []byte(value), []byte("openrouter-api-key"))
	return base64.RawStdEncoding.EncodeToString(sealed), nil
}

func (s *Security) Decrypt(encoded string) (string, error) {
	data, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(data) < s.aead.NonceSize() {
		return "", errors.New("invalid encrypted API key")
	}
	nonce := data[:s.aead.NonceSize()]
	plain, err := s.aead.Open(nil, nonce, data[s.aead.NonceSize():], []byte("openrouter-api-key"))
	if err != nil {
		return "", errors.New("unable to decrypt API key")
	}
	return string(plain), nil
}

func maskSecret(value string) string {
	if len(value) <= 4 {
		return "••••"
	}
	return "••••••••" + value[len(value)-4:]
}
