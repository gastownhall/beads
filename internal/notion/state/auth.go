package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

//nolint:gosec // OAuth token field names must stay aligned with the persisted wire format.
type StoredTokens struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	ExpiresIn    int       `json:"expires_in,omitempty"`
	Scope        string    `json:"scope,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

type AuthStore struct {
	paths *Paths
}

func NewAuthStore(paths *Paths) *AuthStore {
	return &AuthStore{paths: paths}
}

func (s *AuthStore) Paths() *Paths {
	return s.paths
}

func (s *AuthStore) SaveTokens(tokens StoredTokens) error {
	return writeJSON0600(s.paths.TokensPath, tokens)
}

func (s *AuthStore) ReadTokens() (*StoredTokens, error) {
	var tokens StoredTokens
	ok, err := readJSON(s.paths.TokensPath, &tokens)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &tokens, nil
}

func (s *AuthStore) SaveClientInfo(v map[string]any) error {
	return writeJSON0600(s.paths.ClientPath, v)
}

func (s *AuthStore) SaveAuthState(v map[string]any) error {
	return writeJSON0600(s.paths.AuthStatePath, v)
}

func (s *AuthStore) DeleteAuthFilesOnly() error {
	for _, path := range []string{s.paths.ClientPath, s.paths.TokensPath, s.paths.AuthStatePath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *AuthStore) HasTokens() (bool, error) {
	tokens, err := s.ReadTokens()
	if err != nil {
		return false, err
	}
	return tokens != nil && tokens.AccessToken != "", nil
}

func writeJSON0600(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func readJSON(path string, target any) (bool, error) {
	//nolint:gosec // Auth store intentionally reads the configured state file path.
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false, err
	}
	return true, nil
}
