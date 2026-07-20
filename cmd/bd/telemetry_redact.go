package main

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// These expressions recognize password-bearing libpq keyword/value tokens. The
// scrubber is intentionally backend-neutral: command-line telemetry may receive a
// connection string even when bd does not implement that connection's backend.
const dsnWhitespace = `\t\n\f\r \v`

var dsnPasswordValue = regexp.MustCompile(`(?i)(?:^|[` + dsnWhitespace + `])(?:password|sslpassword)[` + dsnWhitespace + `]*=[` + dsnWhitespace + `]*(?:'((?:[^'\\]|\\.)*)'|((?:[^` + dsnWhitespace + `\\]|\\.)+))`)

// A libpq keyword/value connection string begins with a recognized connection
// parameter. Requiring the first key to be recognized avoids treating ordinary
// prose such as "transport=rail password=..." as a DSN while still covering valid
// minimal forms such as "password=..." and service-file forms.
var dsnLeadingKeyword = regexp.MustCompile(`(?i)^[` + dsnWhitespace + `]*(?:application_name|channel_binding|client_encoding|connect_timeout|dbname|fallback_application_name|gssencmode|gsslib|host|hostaddr|keepalives|keepalives_count|keepalives_idle|keepalives_interval|krbsrvname|load_balance_hosts|options|passfile|password|port|replication|requirepeer|service|servicefile|sslcert|sslcrl|sslcrldir|sslkey|sslmode|sslpassword|sslrootcert|sslsni|ssl_max_protocol_version|ssl_min_protocol_version|target_session_attrs|tcp_user_timeout|user)[` + dsnWhitespace + `]*=`)

func scrubStructuredDSNPasswords(dsn, text string) string {
	secrets := structuredDSNPasswords(dsn)
	sort.Slice(secrets, func(i, j int) bool { return len(secrets[i]) > len(secrets[j]) })
	for _, secret := range secrets {
		text = strings.ReplaceAll(text, secret, "xxxxx")
	}
	return text
}

func scrubPotentialDSNPasswords(value string) string {
	if strings.Contains(value, "://") {
		return scrubStructuredDSNPasswords(value, value)
	}
	if dsnLeadingKeyword.MatchString(value) {
		return scrubStructuredDSNPasswords(value, value)
	}
	return value
}

func structuredDSNPasswords(dsn string) []string {
	seen := make(map[string]bool)
	var values []string
	add := func(value string) {
		if value == "" || seen[value] {
			return
		}
		seen[value] = true
		values = append(values, value)
	}

	if parsed, err := url.Parse(dsn); err == nil && parsed.Scheme != "" {
		if parsed.User != nil {
			if password, ok := parsed.User.Password(); ok {
				add(password)
				add(rawURLUserinfoPassword(dsn))
			}
		}
	}
	// Extract query credentials independently of url.Parse. A malformed escape in
	// the path, userinfo, or fragment makes url.Parse reject the entire URL even
	// when the password query pair itself is valid. Telemetry redaction must fail
	// closed in that case.
	addRawURLQueryPasswords(dsn, add)

	for _, match := range dsnPasswordValue.FindAllStringSubmatch(dsn, -1) {
		add(match[1])
		add(match[2])
	}
	return values
}

func addRawURLQueryPasswords(dsn string, add func(string)) {
	queryStart := strings.IndexByte(dsn, '?')
	if queryStart < 0 {
		return
	}
	query := dsn[queryStart+1:]
	if fragment := strings.IndexByte(query, '#'); fragment >= 0 {
		query = query[:fragment]
	}
	for _, pair := range strings.Split(query, "&") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok {
			continue
		}
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			decodedKey = key
		}
		if !isDSNPasswordKey(key) && !isDSNPasswordKey(decodedKey) {
			continue
		}
		add(value)
		if decoded, err := url.QueryUnescape(value); err == nil {
			add(decoded)
		}
	}
}

func isDSNPasswordKey(key string) bool {
	switch strings.ToLower(key) {
	case "password", "sslpassword":
		return true
	default:
		return false
	}
}

func rawURLUserinfoPassword(dsn string) string {
	scheme := strings.Index(dsn, "://")
	if scheme < 0 {
		return ""
	}
	authority := dsn[scheme+len("://"):]
	if end := strings.IndexAny(authority, "/?#"); end >= 0 {
		authority = authority[:end]
	}
	at := strings.LastIndex(authority, "@")
	if at < 0 {
		return ""
	}
	_, password, ok := strings.Cut(authority[:at], ":")
	if !ok {
		return ""
	}
	return password
}
