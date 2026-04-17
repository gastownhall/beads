// Package viper provides a minimal drop-in replacement for github.com/spf13/viper.
// It supports the subset of the viper API used by beads' configuration layer:
// YAML config file loading with merge, environment variable binding, defaults,
// and dot-notation key access.
package viper

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Viper is a minimal configuration store.
type Viper struct {
	configType string
	configFile string

	defaults   map[string]interface{}
	fileValues map[string]interface{} // merged from all config files
	overrides  map[string]interface{} // set via Set()

	envPrefix   string
	envReplacer *strings.Replacer
	autoEnv     bool

	// boundEnv maps config key -> env var name (for BindEnv)
	boundEnv map[string]string

	// inConfig tracks keys that were present in config files
	inConfig map[string]bool
}

// New creates a new Viper instance.
func New() *Viper {
	return &Viper{
		defaults:   make(map[string]interface{}),
		fileValues: make(map[string]interface{}),
		overrides:  make(map[string]interface{}),
		boundEnv:   make(map[string]string),
		inConfig:   make(map[string]bool),
	}
}

func (v *Viper) SetConfigType(ct string) { v.configType = ct }
func (v *Viper) SetConfigFile(cf string) { v.configFile = cf }
func (v *Viper) ConfigFileUsed() string  { return v.configFile }

func (v *Viper) SetEnvPrefix(prefix string)              { v.envPrefix = prefix }
func (v *Viper) SetEnvKeyReplacer(r *strings.Replacer)   { v.envReplacer = r }
func (v *Viper) AutomaticEnv()                            { v.autoEnv = true }

func (v *Viper) BindEnv(key string, envVar ...string) error {
	if len(envVar) > 0 {
		v.boundEnv[key] = envVar[0]
	}
	return nil
}

// ReadInConfig reads the config file and replaces fileValues.
func (v *Viper) ReadInConfig() error {
	data, err := os.ReadFile(v.configFile)
	if err != nil {
		return fmt.Errorf("error reading config file %s: %w", v.configFile, err)
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("error parsing config file %s: %w", v.configFile, err)
	}
	if m == nil {
		m = make(map[string]interface{})
	}
	v.fileValues = m
	v.indexConfigKeys(m, "")
	return nil
}

// MergeInConfig merges the config file into existing fileValues.
func (v *Viper) MergeInConfig() error {
	data, err := os.ReadFile(v.configFile)
	if err != nil {
		return fmt.Errorf("error reading config file %s: %w", v.configFile, err)
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("error parsing config file %s: %w", v.configFile, err)
	}
	if m == nil {
		return nil
	}
	mergeMaps(v.fileValues, m)
	v.indexConfigKeys(m, "")
	return nil
}

func (v *Viper) indexConfigKeys(m map[string]interface{}, prefix string) {
	for k, val := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		v.inConfig[strings.ToLower(key)] = true
		if sub, ok := val.(map[string]interface{}); ok {
			v.indexConfigKeys(sub, key)
		}
	}
}

// SetDefault sets a default value for a key.
func (v *Viper) SetDefault(key string, value interface{}) {
	setNested(v.defaults, strings.ToLower(key), value)
}

// Set sets an override value for a key.
func (v *Viper) Set(key string, value interface{}) {
	setNested(v.overrides, strings.ToLower(key), value)
}

// Get retrieves a value by key. Priority: override > env > file > default.
func (v *Viper) Get(key string) interface{} {
	lk := strings.ToLower(key)

	// 1. overrides
	if val, ok := getNested(v.overrides, lk); ok {
		return val
	}

	// 2. env vars
	if val, ok := v.getEnv(key); ok {
		return val
	}

	// 3. file values
	if val, ok := getNested(v.fileValues, lk); ok {
		return val
	}

	// 4. defaults
	if val, ok := getNested(v.defaults, lk); ok {
		return val
	}

	return nil
}

func (v *Viper) getEnv(key string) (string, bool) {
	// Check explicit BindEnv first
	if envVar, ok := v.boundEnv[key]; ok {
		if val, ok := os.LookupEnv(envVar); ok {
			return val, true
		}
	}

	if !v.autoEnv {
		return "", false
	}

	envKey := key
	if v.envReplacer != nil {
		envKey = v.envReplacer.Replace(envKey)
	}
	envKey = strings.ToUpper(envKey)
	if v.envPrefix != "" {
		envKey = v.envPrefix + "_" + envKey
	}

	if val, ok := os.LookupEnv(envKey); ok {
		return val, true
	}
	return "", false
}

// IsSet returns true if the key has a value from any source.
func (v *Viper) IsSet(key string) bool {
	return v.Get(key) != nil
}

// InConfig returns true if the key was present in config file(s).
func (v *Viper) InConfig(key string) bool {
	return v.inConfig[strings.ToLower(key)]
}

// GetString returns a string value.
func (v *Viper) GetString(key string) string {
	val := v.Get(key)
	if val == nil {
		return ""
	}
	switch s := val.(type) {
	case string:
		return s
	case bool:
		return strconv.FormatBool(s)
	case int:
		return strconv.Itoa(s)
	case int64:
		return strconv.FormatInt(s, 10)
	case float64:
		return strconv.FormatFloat(s, 'f', -1, 64)
	default:
		return fmt.Sprintf("%v", val)
	}
}

// GetBool returns a boolean value.
func (v *Viper) GetBool(key string) bool {
	val := v.Get(key)
	if val == nil {
		return false
	}
	switch b := val.(type) {
	case bool:
		return b
	case string:
		r, _ := strconv.ParseBool(b)
		return r
	case int:
		return b != 0
	case float64:
		return b != 0
	default:
		return false
	}
}

// GetInt returns an int value.
func (v *Viper) GetInt(key string) int {
	val := v.Get(key)
	if val == nil {
		return 0
	}
	switch n := val.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		r, _ := strconv.Atoi(n)
		return r
	case bool:
		if n {
			return 1
		}
		return 0
	default:
		return 0
	}
}

// GetDuration returns a time.Duration value.
func (v *Viper) GetDuration(key string) time.Duration {
	val := v.Get(key)
	if val == nil {
		return 0
	}
	switch d := val.(type) {
	case time.Duration:
		return d
	case string:
		r, _ := time.ParseDuration(d)
		return r
	case int:
		return time.Duration(d)
	case float64:
		return time.Duration(d)
	default:
		return 0
	}
}

// GetStringSlice returns a []string value.
func (v *Viper) GetStringSlice(key string) []string {
	val := v.Get(key)
	if val == nil {
		return nil
	}
	switch s := val.(type) {
	case []string:
		return s
	case []interface{}:
		result := make([]string, 0, len(s))
		for _, item := range s {
			result = append(result, fmt.Sprintf("%v", item))
		}
		return result
	case string:
		if s == "" {
			return nil
		}
		return strings.Split(s, ",")
	default:
		return nil
	}
}

// GetStringMapString returns a map[string]string value.
func (v *Viper) GetStringMapString(key string) map[string]string {
	val := v.Get(key)
	if val == nil {
		return map[string]string{}
	}
	switch m := val.(type) {
	case map[string]string:
		return m
	case map[string]interface{}:
		result := make(map[string]string, len(m))
		for k, v := range m {
			result[k] = fmt.Sprintf("%v", v)
		}
		return result
	default:
		return map[string]string{}
	}
}

// AllSettings returns all settings as a nested map.
func (v *Viper) AllSettings() map[string]interface{} {
	result := make(map[string]interface{})
	copyMap(result, v.defaults)
	copyMap(result, v.fileValues)
	copyMap(result, v.overrides)
	return result
}

// AllKeys returns all known keys in lowercase dot-notation.
func (v *Viper) AllKeys() []string {
	seen := make(map[string]bool)
	collectKeys(v.defaults, "", seen)
	collectKeys(v.fileValues, "", seen)
	collectKeys(v.overrides, "", seen)
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	return keys
}

// --- helpers ---

func setNested(m map[string]interface{}, key string, value interface{}) {
	parts := strings.SplitN(key, ".", 2)
	if len(parts) == 1 {
		m[key] = value
		return
	}
	sub, ok := m[parts[0]].(map[string]interface{})
	if !ok {
		sub = make(map[string]interface{})
		m[parts[0]] = sub
	}
	setNested(sub, parts[1], value)
}

func getNested(m map[string]interface{}, key string) (interface{}, bool) {
	parts := strings.SplitN(key, ".", 2)
	val, ok := m[parts[0]]
	if !ok {
		return nil, false
	}
	if len(parts) == 1 {
		return val, true
	}
	sub, ok := val.(map[string]interface{})
	if !ok {
		return nil, false
	}
	return getNested(sub, parts[1])
}

func mergeMaps(dst, src map[string]interface{}) {
	for k, sv := range src {
		if dv, ok := dst[k]; ok {
			dSub, dOk := dv.(map[string]interface{})
			sSub, sOk := sv.(map[string]interface{})
			if dOk && sOk {
				mergeMaps(dSub, sSub)
				continue
			}
		}
		dst[k] = sv
	}
}

func copyMap(dst, src map[string]interface{}) {
	mergeMaps(dst, src)
}

func collectKeys(m map[string]interface{}, prefix string, seen map[string]bool) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		seen[key] = true
		if sub, ok := v.(map[string]interface{}); ok {
			collectKeys(sub, key, seen)
		}
	}
}
