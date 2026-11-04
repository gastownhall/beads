package uow

import (
	"errors"
	"testing"
)

func TestLocalDoltNeverListened(t *testing.T) {
	t.Parallel()
	if localDoltNeverListened(nil) {
		t.Fatal("nil error is not a server that never listened")
	}
	if !localDoltNeverListened(errors.New("uow: ping db: dial tcp 127.0.0.1:33619: connect: connection refused")) {
		t.Fatal("connection refused is a server that never listened")
	}
	if !localDoltNeverListened(errors.New("dolt sql-server exited before listener became ready")) {
		t.Fatal("exit before listen is a server that never listened")
	}
	if localDoltNeverListened(errors.New("uow: init schema: duplicate column")) {
		t.Fatal("a schema error is not a server that never listened")
	}
}
