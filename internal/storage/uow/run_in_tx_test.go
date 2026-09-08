package uow

import (
	"context"
	"errors"
	"testing"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/steveyegge/beads/internal/storage/domain"
)

// stubUOW implements UnitOfWork for testing. Only Commit and Close are used;
// all use-case accessors panic if called (they shouldn't be in these tests).
type stubUOW struct {
	commitErr error
	closed    bool
}

func (u *stubUOW) Close(_ context.Context)                     { u.closed = true }
func (u *stubUOW) Commit(_ context.Context, _ string) error    { return u.commitErr }
func (u *stubUOW) ConfigUseCase() domain.ConfigUseCase         { panic("not implemented") }
func (u *stubUOW) DoltRemoteUseCase() domain.DoltRemoteUseCase { panic("not implemented") }
func (u *stubUOW) BootstrapUseCase() domain.BootstrapUseCase   { panic("not implemented") }
func (u *stubUOW) IssueUseCase() domain.IssueUseCase           { panic("not implemented") }
func (u *stubUOW) DependencyUseCase() domain.DependencyUseCase { panic("not implemented") }
func (u *stubUOW) LabelUseCase() domain.LabelUseCase           { panic("not implemented") }
func (u *stubUOW) CommentUseCase() domain.CommentUseCase       { panic("not implemented") }

// controlledProvider lets tests inject errors per-attempt.
type controlledProvider struct {
	attempts   int
	newUOWErrs []error // per-attempt NewUOW error (nil = success)
	commitErrs []error // per-attempt Commit error (nil = success)
}

func (p *controlledProvider) Close(_ context.Context) error { return nil }

func (p *controlledProvider) NewUOW(_ context.Context) (UnitOfWork, error) {
	i := p.attempts
	p.attempts++
	var newErr error
	if i < len(p.newUOWErrs) {
		newErr = p.newUOWErrs[i]
	}
	if newErr != nil {
		return nil, newErr
	}
	var commitErr error
	if i < len(p.commitErrs) {
		commitErr = p.commitErrs[i]
	}
	return &stubUOW{commitErr: commitErr}, nil
}

func TestRunInTx_Success(t *testing.T) {
	p := &controlledProvider{}
	err := RunInTx(context.Background(), p, "test", func(_ UnitOfWork) error {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.attempts != 1 {
		t.Errorf("expected 1 attempt, got %d", p.attempts)
	}
}

func TestRunInTx_RetryOnSerializationError(t *testing.T) {
	deadlock := &mysql.MySQLError{Number: 1213, Message: "Deadlock found when trying to get lock"}
	p := &controlledProvider{commitErrs: []error{deadlock, nil}}
	err := RunInTx(context.Background(), p, "test", func(_ UnitOfWork) error {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.attempts != 2 {
		t.Errorf("expected 2 attempts (1 retry), got %d", p.attempts)
	}
}

// A connection loss AT COMMIT is ambiguous (the commit may have landed before
// the connection dropped), so it must NOT be retried — retrying could create a
// duplicate. RunInTx returns ErrCommitIndeterminate after a single attempt.
func TestRunInTx_CommitConnectionLossIsIndeterminate(t *testing.T) {
	p := &controlledProvider{commitErrs: []error{errors.New("invalid connection"), nil}}
	err := RunInTx(context.Background(), p, "test", func(_ UnitOfWork) error {
		return nil
	})
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("expected ErrCommitIndeterminate, got: %v", err)
	}
	if p.attempts != 1 {
		t.Errorf("commit-phase connection loss must not be retried: got %d attempts", p.attempts)
	}
}

// A transient connection error raised INSIDE fn (before commit) leaves nothing
// committed, so the whole sequence is safe to replay.
func TestRunInTx_RetryOnTransientErrorInFn(t *testing.T) {
	p := &controlledProvider{}
	fnCalls := 0
	err := RunInTx(context.Background(), p, "test", func(_ UnitOfWork) error {
		fnCalls++
		if fnCalls == 1 {
			return errors.New("invalid connection")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fnCalls != 2 {
		t.Errorf("pre-commit transient in fn must be retried: fn called %d times", fnCalls)
	}
}

func TestRunInTx_RetryOnNewUOWConnectionError(t *testing.T) {
	p := &controlledProvider{newUOWErrs: []error{errors.New("driver: bad connection"), nil}}
	err := RunInTx(context.Background(), p, "test", func(_ UnitOfWork) error {
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.attempts != 2 {
		t.Errorf("expected 2 attempts (1 retry), got %d", p.attempts)
	}
}

func TestRunInTx_NoDomainErrorRetry(t *testing.T) {
	domainErr := errors.New("ErrAlreadyClaimed")
	fnCalls := 0
	p := &controlledProvider{}
	err := RunInTx(context.Background(), p, "test", func(_ UnitOfWork) error {
		fnCalls++
		return domainErr
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if fnCalls != 1 {
		t.Errorf("domain error must not be retried: fn called %d times", fnCalls)
	}
}

func TestRunInTx_NothingToCommitIsSuccess(t *testing.T) {
	p := &controlledProvider{commitErrs: []error{errors.New("nothing to commit")}}
	err := RunInTx(context.Background(), p, "test", func(_ UnitOfWork) error {
		return nil
	})
	if err != nil {
		t.Fatalf("nothing-to-commit should be treated as success, got: %v", err)
	}
}

// RunInTxMsg shares the phase-aware commit handling: a commit-phase connection
// loss is indeterminate and not retried.
func TestRunInTxMsg_CommitConnectionLossIsIndeterminate(t *testing.T) {
	p := &controlledProvider{commitErrs: []error{errors.New("invalid connection"), nil}}
	err := RunInTxMsg(context.Background(), p, func(_ UnitOfWork) (string, error) {
		return "test commit", nil
	})
	if !errors.Is(err, ErrCommitIndeterminate) {
		t.Fatalf("expected ErrCommitIndeterminate, got: %v", err)
	}
	if p.attempts != 1 {
		t.Errorf("commit-phase connection loss must not be retried: got %d attempts", p.attempts)
	}
}
