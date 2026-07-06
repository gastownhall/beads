package configfile

import "testing"

func TestExternalDoltConfig_ResolvedUser(t *testing.T) {
	t.Run("empty falls back to default", func(t *testing.T) {
		if got := (ExternalDoltConfig{}).ResolvedUser(); got != ExternalDoltConfigDefaultUser {
			t.Errorf("got %q, want %q", got, ExternalDoltConfigDefaultUser)
		}
	})

	t.Run("explicit user passes through", func(t *testing.T) {
		if got := (ExternalDoltConfig{User: "beads"}).ResolvedUser(); got != "beads" {
			t.Errorf("got %q, want %q", got, "beads")
		}
	})
}
