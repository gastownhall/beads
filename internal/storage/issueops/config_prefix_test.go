package issueops

import "testing"

// ConfigPrefixPattern must escape every LIKE metacharacter so a prefix always
// matches literally: an unescaped `_` in "kv.a_." would also match "kv.axb".
func TestConfigPrefixPattern(t *testing.T) {
	cases := []struct{ in, want string }{
		{"kv.mail.dog.", "kv.mail.dog.%"},
		{"kv.a_.", "kv.a!_.%"},
		{"kv.a%.", "kv.a!%.%"},
		{"kv.a!.", "kv.a!!.%"},
		{"", "%"},
	}
	for _, c := range cases {
		if got := ConfigPrefixPattern(c.in); got != c.want {
			t.Errorf("ConfigPrefixPattern(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
