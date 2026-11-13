package domain

import (
	"context"
	"reflect"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// stubConfigRepo is a ConfigSQLRepository offering only GetAllConfig.
type stubConfigRepo struct {
	ConfigSQLRepository
	all map[string]string
}

func (r *stubConfigRepo) GetAllConfig(context.Context) (map[string]string, error) {
	return r.all, nil
}

// stubKeyedConfigRepo also offers the keyed read, recording what it is asked.
type stubKeyedConfigRepo struct {
	stubConfigRepo
	asked [][]string
}

func (r *stubKeyedConfigRepo) GetConfigKeys(_ context.Context, keys ...string) (map[string]string, error) {
	r.asked = append(r.asked, keys)
	return map[string]string{"from.keyed.read": "yes"}, nil
}

// TestConfigUseCaseGetConfigKeys pins both arms of the use case: it hands the
// keys to a repository that has the keyed read, and picks exact keys out of
// GetAllConfig for one that does not.
func TestConfigUseCaseGetConfigKeys(t *testing.T) {
	ctx := context.Background()

	t.Run("forwards to a repository with the keyed read", func(t *testing.T) {
		repo := &stubKeyedConfigRepo{}
		got, err := NewConfigUseCase(repo).(ConfigKeysReader).GetConfigKeys(ctx, "a", "b")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, map[string]string{"from.keyed.read": "yes"}) {
			t.Fatalf("GetConfigKeys = %v, want the repository's answer", got)
		}
		if !reflect.DeepEqual(repo.asked, [][]string{{"a", "b"}}) {
			t.Fatalf("repository was asked %v, want one read of both keys", repo.asked)
		}
	})

	t.Run("falls back to exact keys from the whole table", func(t *testing.T) {
		repo := &stubConfigRepo{all: map[string]string{"routing.mode": "auto", "kv.memory.notes": "large"}}
		reader := NewConfigUseCase(repo).(ConfigKeysReader)
		got, err := reader.GetConfigKeys(ctx, "routing.mode", "absent", "ROUTING.MODE")
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, map[string]string{"routing.mode": "auto"}) {
			t.Fatalf("GetConfigKeys = %v, want only the exact key that has a row", got)
		}
		if got, err := reader.GetConfigKeys(ctx); err != nil || len(got) != 0 {
			t.Fatalf("GetConfigKeys() = %v, %v, want an empty map", got, err)
		}
	})
}

func TestDefaultInfraTypesExcludeRig(t *testing.T) {
	got := DefaultInfraTypes()
	want := []string{"agent", "role", "message"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DefaultInfraTypes() = %v, want %v", got, want)
	}
	if IsInfraType(types.IssueType("rig")) {
		t.Fatal("rig must be durable by default, not an infra/wisp-routed type")
	}
}

func TestDefaultInfraTypesReturnsCopy(t *testing.T) {
	got := DefaultInfraTypes()
	got[0] = "mutated"

	if IsInfraType(types.IssueType("mutated")) {
		t.Fatal("mutating DefaultInfraTypes result changed infra classification")
	}
	if !IsInfraType(types.IssueType("agent")) {
		t.Fatal("agent should remain an infra type")
	}
}
