package flatfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/steveyegge/beads/internal/types"
)

// SetConfig sets a key-value pair in the config store. status.custom and
// types.custom are validated and synced to the normalized custom-status/
// custom-type files first, mirroring the SQL backends' SetConfig →
// SyncCustomStatusesTable/SyncCustomTypesTable write tx: an invalid value
// returns an error and nothing is stored. runAtomic makes the two-file write
// match that single write tx: concurrent setters serialize (no interleave can
// leave the config value and the normalized file disagreeing) and a partial
// failure rolls both files back.
func (s *FlatFileStore) SetConfig(ctx context.Context, key, value string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	if strings.HasPrefix(key, metadataKeyPrefix) {
		// The SQL backends keep metadata in its own table, so 'bd config set
		// metadata:x' could never clobber it; flatfile shares config_kv.json,
		// so the prefix is reserved (see metadataKeyPrefix). Erroring loudly
		// beats silently overwriting the repo fingerprint.
		return fmt.Errorf("flatfile: config key %q uses the reserved %q prefix", key, metadataKeyPrefix)
	}
	return s.runAtomic(ctx, func() error {
		switch key {
		case "status.custom":
			if err := s.syncCustomStatuses(value); err != nil {
				return err
			}
		case "types.custom":
			if err := s.syncCustomTypes(value); err != nil {
				return err
			}
		}
		return s.setKV(s.configKVPath, key, value)
	})
}

// metadataKeyPrefix namespaces Get/SetMetadata entries inside config_kv.json.
// The SQL backends store metadata in a separate table, so the config API must
// stay blind to this prefix: GetConfig/GetAllConfig never return it,
// SetConfig rejects it, and DeleteConfig ignores it.
const metadataKeyPrefix = "metadata:"

// GetConfig retrieves a value from the config store. Metadata-namespaced keys
// read as absent, as on the SQL backends where the config table has no such row.
func (s *FlatFileStore) GetConfig(_ context.Context, key string) (string, error) {
	if err := s.checkClosed(); err != nil {
		return "", err
	}
	if strings.HasPrefix(key, metadataKeyPrefix) {
		return "", nil
	}
	return s.getKV(s.configKVPath, key)
}

// GetAllConfig returns all config key-value pairs. Metadata-namespaced keys
// are filtered out: GetAllConfigInTx selects only FROM config, so 'bd config
// list' must not leak metadata:_project_id and friends.
func (s *FlatFileStore) GetAllConfig(_ context.Context) (map[string]string, error) {
	if err := s.checkClosed(); err != nil {
		return nil, err
	}
	kv, err := s.readKVFile(s.configKVPath)
	if err != nil {
		return nil, err
	}
	for k := range kv {
		if strings.HasPrefix(k, metadataKeyPrefix) {
			delete(kv, k)
		}
	}
	return kv, nil
}

// SetLocalMetadata sets a key-value in the local (non-git-tracked) metadata store.
func (s *FlatFileStore) SetLocalMetadata(_ context.Context, key, value string) error {
	if err := s.checkClosed(); err != nil {
		return err
	}
	defer s.txShared()()
	return s.setKV(s.localMetaPath, key, value)
}

// GetLocalMetadata retrieves a value from the local metadata store.
// Returns ("", nil) if the key does not exist.
func (s *FlatFileStore) GetLocalMetadata(_ context.Context, key string) (string, error) {
	if err := s.checkClosed(); err != nil {
		return "", err
	}
	return s.getKV(s.localMetaPath, key)
}

// readKVFile reads a JSON key-value file.
func (s *FlatFileStore) readKVFile(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return make(map[string]string), nil
		}
		return nil, fmt.Errorf("flatfile: read kv %s: %w", path, err)
	}

	var kv map[string]string
	if err := json.Unmarshal(data, &kv); err != nil {
		return nil, fmt.Errorf("flatfile: parse kv %s: %w", path, err)
	}
	if kv == nil {
		kv = make(map[string]string)
	}
	return kv, nil
}

// writeKVFile writes a JSON key-value file atomically.
func (s *FlatFileStore) writeKVFile(path string, kv map[string]string) error {
	data, err := json.MarshalIndent(kv, "", "  ")
	if err != nil {
		return fmt.Errorf("flatfile: marshal kv: %w", err)
	}
	data = append(data, '\n')

	if err := s.atomicWrite(path, data); err != nil {
		return fmt.Errorf("flatfile: write kv %s: %w", path, err)
	}
	return nil
}

func (s *FlatFileStore) setKV(path, key, value string) error {
	s.kvMu.Lock()
	defer s.kvMu.Unlock()

	kv, err := s.readKVFile(path)
	if err != nil {
		return err
	}
	kv[key] = value
	return s.writeKVFile(path, kv)
}

func (s *FlatFileStore) getKV(path, key string) (string, error) {
	kv, err := s.readKVFile(path)
	if err != nil {
		return "", err
	}
	return kv[key], nil
}

// syncCustomStatuses replaces the normalized custom-status file with the
// parsed config value. Mirrors issueops.SyncCustomStatusesTable: parsing
// validates status names and categories.
func (s *FlatFileStore) syncCustomStatuses(value string) error {
	var parsed []types.CustomStatus
	if value != "" {
		var err error
		parsed, err = types.ParseCustomStatusConfig(value)
		if err != nil {
			return fmt.Errorf("invalid status.custom value: %w", err)
		}
	}
	return s.writeJSONFile(s.customStatusesPath, parsed)
}

// syncCustomTypes replaces the normalized custom-type file with the parsed
// config value. Mirrors issueops.SyncCustomTypesTable.
func (s *FlatFileStore) syncCustomTypes(value string) error {
	return s.writeJSONFile(s.customTypesPath, parseTypesValue(value))
}

// parseTypesValue accepts a JSON string array or a comma-separated list,
// mirroring issueops.parseTypesValue.
func parseTypesValue(value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	var jsonTypes []string
	if err := json.Unmarshal([]byte(value), &jsonTypes); err == nil {
		return jsonTypes
	}
	var out []string
	for _, part := range strings.Split(value, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// readCustomStatuses reads the normalized custom-status file. A missing
// file means no custom statuses are configured.
func (s *FlatFileStore) readCustomStatuses() ([]types.CustomStatus, error) {
	var out []types.CustomStatus
	if err := readJSONFile(s.customStatusesPath, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// readCustomTypes reads the normalized custom-type file. A missing file
// means no custom types are configured.
func (s *FlatFileStore) readCustomTypes() ([]string, error) {
	var out []string
	if err := readJSONFile(s.customTypesPath, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// writeJSONFile writes v as indented JSON atomically (write-then-rename).
// It is a store method so the write is journaled for transaction rollback.
func (s *FlatFileStore) writeJSONFile(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("flatfile: marshal %s: %w", path, err)
	}
	data = append(data, '\n')

	if err := s.atomicWrite(path, data); err != nil {
		return fmt.Errorf("flatfile: write %s: %w", path, err)
	}
	return nil
}

// readJSONFile unmarshals path into v; a missing file leaves v untouched.
func readJSONFile(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("flatfile: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("flatfile: parse %s: %w", path, err)
	}
	return nil
}
