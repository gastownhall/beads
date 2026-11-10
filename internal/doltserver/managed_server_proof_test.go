package doltserver

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/steveyegge/beads/internal/lockfile"
	"github.com/steveyegge/beads/internal/testutil"
)

// TestManagesLiveServerOnPort pins the ownership proof: both state files, a
// live PID, and a port file agreeing with the port the caller is connected on.
//
// Every "false" case below is a silent in-place migration of somebody else's
// database if it ever flips, so each one names the topology it stands for.
func TestManagesLiveServerOnPort(t *testing.T) {
	const port = 3307

	// dead is a PID no live process answers to: above every default pid_max.
	const dead = 2147483646

	newDir := func(t *testing.T) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), ".beads")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		return dir
	}
	write := func(t *testing.T, dir, name string, v int) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strconv.Itoa(v)), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	// The PID recorded here is this test binary's, which is emphatically not a
	// dolt sql-server: parseDoltProcessPIDs requires "dolt" followed by
	// "sql-server" in the command line, so an identity check would never return
	// it. That is the point — this subtest pins the predicate's liveness-only
	// contract and is the live demonstration that any live PID beside an
	// agreeing port file is accepted today, dolt or not. #6123 is expected to
	// tighten that (a distinct adoption marker, or an identity check); when it
	// does, this subtest goes red, and that is the signal to re-derive #6088's
	// guarantee from whatever the new proof is, not to relax the assertion.
	t.Run("any live pid with an agreeing port file is proof", func(t *testing.T) {
		dir := newDir(t)
		write(t, dir, PortFileName, port)
		write(t, dir, PIDFileName, os.Getpid())
		if !ManagesLiveServerOnPort(dir, port) {
			t.Fatal("a workspace with a live recorded server on this port owns it (#6088 must keep migrating)")
		}
	})

	t.Run("dead pid is not proof", func(t *testing.T) {
		// The workspace once ran its own server on this port; it died, and an
		// externally managed server now answers there. 3307 is both the common
		// external port and bd's own default, so the collision is likely.
		dir := newDir(t)
		write(t, dir, PortFileName, port)
		write(t, dir, PIDFileName, dead)
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("a port file records that bd once bound a port, not that bd answers on it now")
		}
	})

	t.Run("port file naming a different port is not proof", func(t *testing.T) {
		dir := newDir(t)
		write(t, dir, PortFileName, 40083)
		write(t, dir, PIDFileName, os.Getpid())
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("bd's server is on another port; this endpoint is somebody else's")
		}
	})

	t.Run("missing state files are not proof", func(t *testing.T) {
		dir := newDir(t)
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("bd never started a server here")
		}
		write(t, dir, PortFileName, port)
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("a port file with no pid file proves nothing about liveness")
		}
	})

	t.Run("garbage pid file is not proof", func(t *testing.T) {
		dir := newDir(t)
		write(t, dir, PortFileName, port)
		if err := os.WriteFile(filepath.Join(dir, PIDFileName), []byte("not-a-pid"), 0o600); err != nil {
			t.Fatalf("write pid: %v", err)
		}
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("an unparseable pid file must fail closed")
		}
	})

	t.Run("degenerate inputs fail closed", func(t *testing.T) {
		if ManagesLiveServerOnPort("", port) {
			t.Fatal("no workspace, no proof")
		}
		if ManagesLiveServerOnPort(newDir(t), 0) {
			t.Fatal("port 0 means unresolved; there is no endpoint to own")
		}
	})

	// Read-only: unlike IsRunning, classifying a connection must never repair
	// or delete a workspace's server state as a side effect.
	t.Run("does not mutate workspace state", func(t *testing.T) {
		dir := newDir(t)
		write(t, dir, PortFileName, port)
		write(t, dir, PIDFileName, dead)
		if ManagesLiveServerOnPort(dir, port) {
			t.Fatal("dead pid must not be proof")
		}
		for _, name := range []string{PortFileName, PIDFileName} {
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Fatalf("%s was removed: %v — the predicate must not repair state it merely reads", name, err)
			}
		}
	})
}

// TestServesDatabaseDirOnPort pins the served-directory proof the CLI routes
// take, the dolt CLI's own test for handing a command to a server: a dolt
// process holds the database's lock, and the first sql-server.info the dolt
// CLI would find for the database directory, in its own .dolt or else its
// parent's, names the dialed port and a process that is live by the dolt
// CLI's test. A dead process's file ends the search, since the dolt CLI
// command bd runs removes it and works on the directory's files without
// reading the parent's. A copy of a served database carries its server's
// file, but nothing holds its lock, so it is no server's; nor is a directory
// a stopped server left its file in, whatever process the file's PID names
// now. The proof reads no process list, which can be refused (GH#7214).
func TestServesDatabaseDirOnPort(t *testing.T) {
	const port = 3307
	const dead = 2147483646
	live := os.Getpid()
	p := strconv.Itoa(port)
	info := func(t *testing.T, dir string, pid int, port string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".dolt", ServerInfoFileName), []byte(strconv.Itoa(pid)+":"+port+":secret"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// locked holds db's lock as a dolt process that has it open does.
	locked := func(t *testing.T, db string) { testutil.HoldDoltDatabaseLock(t, db) }
	// unlocked gives db the lock file a copy of a served database carries,
	// which no process holds.
	unlocked := func(t *testing.T, db string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(db, ".dolt", "noms"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(db, ".dolt", "noms", "LOCK"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	newDB := func(t *testing.T) (root, db string) {
		root = filepath.Join(t.TempDir(), "dolt")
		db = filepath.Join(root, "testdb")
		if err := os.MkdirAll(filepath.Join(db, ".dolt"), 0o700); err != nil {
			t.Fatal(err)
		}
		return root, db
	}
	// noProcessList fails the test if the proof reads the process list.
	noProcessList := func(t *testing.T) {
		stubProcessList(t, func() ([]int, error) {
			t.Error("the served-directory proof read the process list")
			return nil, errProcessListUnreadable
		})
	}
	type testCase struct {
		name  string
		setup func(t *testing.T, root, db string)
		port  int
		want  bool
	}
	cases := []testCase{
		{"nothing serves it", func(t *testing.T, root, db string) {}, port, false},
		{"multi-database server on the dialed port", func(t *testing.T, root, db string) {
			info(t, root, live, p)
			locked(t, db)
		}, port, true},
		{"server started in the database itself", func(t *testing.T, root, db string) {
			info(t, db, live, p)
			locked(t, db)
		}, port, true},
		// A cp -a of .beads/dolt taken while its server ran: the server's
		// file names the dialed port and a live process, and nothing holds
		// the copy's lock, so the dolt CLI works on the copy's files.
		{"a copy of a served database", func(t *testing.T, root, db string) {
			info(t, root, live, p)
			unlocked(t, db)
		}, port, false},
		{"a server's file beside a database no dolt process opened", func(t *testing.T, root, db string) { info(t, root, live, p) }, port, false},
		// A dolt process has the database open, with no server's file for
		// the dolt CLI to hand a command to.
		{"locked, with no server info file", func(t *testing.T, root, db string) { locked(t, db) }, port, false},
		{"server on another port", func(t *testing.T, root, db string) {
			info(t, root, live, "3308")
			locked(t, db)
		}, port, false},
		// The file's server has exited while another dolt process holds the
		// lock.
		{"server gone", func(t *testing.T, root, db string) {
			info(t, root, dead, p)
			locked(t, db)
		}, port, false},
		{"socket-only server", func(t *testing.T, root, db string) {
			info(t, root, live, "-")
			locked(t, db)
		}, port, false},
		{"first live file found decides", func(t *testing.T, root, db string) {
			info(t, db, live, "3308")
			info(t, root, live, p)
			locked(t, db)
		}, port, false},
		{"a dead server's file in the database ends the search", func(t *testing.T, root, db string) {
			info(t, db, dead, p)
			info(t, root, live, p)
			locked(t, db)
		}, port, false},
		{"a dead socket-only server's file in the database ends the search", func(t *testing.T, root, db string) {
			info(t, db, dead, "-")
			info(t, root, live, p)
			locked(t, db)
		}, port, false},
		{"a dead server's file in the database, nothing in its parent", func(t *testing.T, root, db string) {
			info(t, db, dead, p)
			locked(t, db)
		}, port, false},
		{"no dialed port", func(t *testing.T, root, db string) {
			info(t, root, live, "0")
			locked(t, db)
		}, 0, false},
	}
	if runtime.GOOS != "windows" {
		// PID 1 is root's: a signal from an unprivileged test fails with
		// EPERM, which the dolt CLI takes as a live process. Beside a held
		// lock it is a server another account runs. Beside a lock nobody
		// holds it is the host's init, named by a file a stopped container
		// server left on a bind mount.
		cases = append(cases,
			testCase{"a server of another user", func(t *testing.T, root, db string) {
				info(t, root, 1, p)
				locked(t, db)
			}, port, true},
			testCase{"a stopped container server's file naming the host's PID 1", func(t *testing.T, root, db string) {
				info(t, root, 1, p)
				unlocked(t, db)
			}, port, false})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			noProcessList(t)
			root, db := newDB(t)
			tc.setup(t, root, db)
			if got := ServesDatabaseDirOnPort(db, tc.port); got != tc.want {
				t.Errorf("ServesDatabaseDirOnPort = %v, want %v", got, tc.want)
			}
		})
	}

	// Read-only: asking creates no lock file and leaves a free lock free.
	t.Run("does not take or create the lock", func(t *testing.T) {
		noProcessList(t)
		root, db := newDB(t)
		info(t, root, live, p)
		lockPath := filepath.Join(db, ".dolt", "noms", "LOCK")
		ServesDatabaseDirOnPort(db, port)
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Fatalf("the proof created %s (stat error: %v)", lockPath, err)
		}
		unlocked(t, db)
		ServesDatabaseDirOnPort(db, port)
		f, err := os.OpenFile(lockPath, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if err := lockfile.FlockExclusiveNonBlocking(f); err != nil {
			t.Fatalf("the database's lock is still taken after the proof: %v", err)
		}
		_ = lockfile.FlockUnlock(f)
	})
}
