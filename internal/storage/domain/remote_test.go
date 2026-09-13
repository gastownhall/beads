package domain

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeRemoteRepo struct {
	remotes []Remote
	calls   []string
	failAdd string
	listErr error
}

func (f *fakeRemoteRepo) AddRemote(ctx context.Context, name, url string) error {
	return f.AddRemoteWithRef(ctx, name, url, "")
}

func (f *fakeRemoteRepo) AddRemoteWithRef(_ context.Context, name, url, ref string) error {
	f.calls = append(f.calls, "add "+name+" "+url+" ref="+ref)
	if url == f.failAdd {
		return errors.New("refused")
	}
	f.remotes = append(f.remotes, Remote{Name: name, URL: url, Ref: ref})
	return nil
}

func (f *fakeRemoteRepo) RemoveRemote(_ context.Context, name string) error {
	f.calls = append(f.calls, "remove "+name)
	kept := f.remotes[:0]
	for _, r := range f.remotes {
		if r.Name != name {
			kept = append(kept, r)
		}
	}
	f.remotes = kept
	return nil
}

func (f *fakeRemoteRepo) ListRemotes(context.Context) ([]Remote, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]Remote(nil), f.remotes...), nil
}

// A remote's git data ref survives UpdateRemote: the replacement carries it,
// and so does the restore after a failed replacement. Both ref shapes.
func TestUpdateRemoteKeepsGitDataRef(t *testing.T) {
	for _, ref := range []string{"refs/heads/issue-data", "refs/dolt/units/team-12542"} {
		t.Run(ref, func(t *testing.T) {
			repo := &fakeRemoteRepo{remotes: []Remote{{Name: "origin", URL: "git+https://example.com/old.git", Ref: ref}}}
			uc := NewDoltRemoteUseCase(repo)
			if err := uc.UpdateRemote(context.Background(), "origin", "git+https://example.com/new.git"); err != nil {
				t.Fatalf("UpdateRemote: %v", err)
			}
			want := []string{"remove origin", "add origin git+https://example.com/new.git ref=" + ref}
			if !reflect.DeepEqual(repo.calls, want) {
				t.Fatalf("calls = %v, want %v", repo.calls, want)
			}
		})
	}

	t.Run("restore after a failed add keeps the ref", func(t *testing.T) {
		repo := &fakeRemoteRepo{
			remotes: []Remote{{Name: "origin", URL: "git+https://example.com/old.git", Ref: "refs/dolt/units/team-12542"}},
			failAdd: "git+https://example.com/new.git",
		}
		uc := NewDoltRemoteUseCase(repo)
		if err := uc.UpdateRemote(context.Background(), "origin", "git+https://example.com/new.git"); err == nil {
			t.Fatal("UpdateRemote should report the failed add")
		}
		if len(repo.remotes) != 1 || repo.remotes[0].Ref != "refs/dolt/units/team-12542" || repo.remotes[0].URL != "git+https://example.com/old.git" {
			t.Fatalf("restored remote = %+v, want the old URL on the old ref", repo.remotes)
		}
	})

	t.Run("a failed listing stops the update before removal", func(t *testing.T) {
		repo := &fakeRemoteRepo{
			remotes: []Remote{{Name: "origin", URL: "git+https://example.com/old.git", Ref: "refs/dolt/units/team-12542"}},
			listErr: errors.New("server unreachable"),
		}
		uc := NewDoltRemoteUseCase(repo)
		if err := uc.UpdateRemote(context.Background(), "origin", "git+https://example.com/new.git"); err == nil {
			t.Fatal("UpdateRemote should fail when the listing fails")
		}
		if len(repo.calls) != 0 {
			t.Fatalf("nothing may be removed or added after a failed listing, got %v", repo.calls)
		}
	})

	t.Run("a move to a non-git URL drops the ref", func(t *testing.T) {
		repo := &fakeRemoteRepo{remotes: []Remote{{Name: "origin", URL: "git+https://example.com/old.git", Ref: "refs/dolt/units/team-12542"}}}
		uc := NewDoltRemoteUseCase(repo)
		if err := uc.UpdateRemote(context.Background(), "origin", "dolthub://org/repo"); err != nil {
			t.Fatalf("UpdateRemote: %v", err)
		}
		if want := []string{"remove origin", "add origin dolthub://org/repo ref="}; !reflect.DeepEqual(repo.calls, want) {
			t.Fatalf("calls = %v, want %v", repo.calls, want)
		}
	})

	t.Run("remote on the default ref stays on it", func(t *testing.T) {
		repo := &fakeRemoteRepo{remotes: []Remote{{Name: "origin", URL: "file:///tmp/old"}}}
		uc := NewDoltRemoteUseCase(repo)
		if err := uc.UpdateRemote(context.Background(), "origin", "file:///tmp/new"); err != nil {
			t.Fatalf("UpdateRemote: %v", err)
		}
		if want := []string{"remove origin", "add origin file:///tmp/new ref="}; !reflect.DeepEqual(repo.calls, want) {
			t.Fatalf("calls = %v, want %v", repo.calls, want)
		}
	})
}

func TestCreateRemoteWithRefValidatesArguments(t *testing.T) {
	repo := &fakeRemoteRepo{}
	uc := NewDoltRemoteUseCase(repo)
	if err := uc.CreateRemoteWithRef(context.Background(), "", "file:///tmp/x", "refs/heads/issue-data"); err == nil {
		t.Error("empty name should be refused")
	}
	if err := uc.CreateRemoteWithRef(context.Background(), "origin", "", "refs/heads/issue-data"); err == nil {
		t.Error("empty url should be refused")
	}
	if err := uc.CreateRemoteWithRef(context.Background(), "origin", "git+file:///srv/ledgers", "refs/dolt/units/k"); err != nil {
		t.Fatalf("CreateRemoteWithRef: %v", err)
	}
	if want := []string{"add origin git+file:///srv/ledgers ref=refs/dolt/units/k"}; !reflect.DeepEqual(repo.calls, want) {
		t.Fatalf("calls = %v, want %v", repo.calls, want)
	}
}
