package server

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExternalDoltConfigValidate(t *testing.T) {
	t.Run("tcp endpoint with host+port", func(t *testing.T) {
		require.NoError(t, configfile.ExternalDoltConfig{Host: "db.internal", Port: 3306}.Validate())
	})

	t.Run("unix socket endpoint", func(t *testing.T) {
		require.NoError(t, configfile.ExternalDoltConfig{Socket: "/var/run/dolt.sock"}.Validate())
	})

	t.Run("tcp endpoint with tls required and no cert/key", func(t *testing.T) {
		require.NoError(t, configfile.ExternalDoltConfig{
			Host:        "hosted-dolt.example.com",
			Port:        3306,
			TLSRequired: true,
		}.Validate())
	})

	t.Run("tcp endpoint with paired tls cert+key", func(t *testing.T) {
		require.NoError(t, configfile.ExternalDoltConfig{
			Host:        "hosted-dolt.example.com",
			Port:        3306,
			TLSRequired: true,
			TLSCert:     "/etc/beads/client.pem",
			TLSKey:      "/etc/beads/client.key",
		}.Validate())
	})

	t.Run("keep alive period zero is fine", func(t *testing.T) {
		require.NoError(t, configfile.ExternalDoltConfig{Host: "db", Port: 3306, KeepAlivePeriod: 0}.Validate())
	})

	t.Run("keep alive period positive is fine", func(t *testing.T) {
		require.NoError(t, configfile.ExternalDoltConfig{Host: "db", Port: 3306, KeepAlivePeriod: 60 * time.Second}.Validate())
	})

	t.Run("empty config rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must set Socket or (Host, Port)")
	})

	t.Run("socket and host together rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{Host: "db", Port: 3306, Socket: "/var/run/dolt.sock"}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "either Socket OR (Host, Port)")
	})

	t.Run("socket and port together rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{Port: 3306, Socket: "/var/run/dolt.sock"}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "either Socket OR (Host, Port)")
	})

	t.Run("host without port rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{Host: "db"}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Host requires Port")
	})

	t.Run("port without host rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{Port: 3306}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Port requires Host")
	})

	t.Run("port zero with host rejected (treated as missing)", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{Host: "db", Port: 0}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Host requires Port")
	})

	t.Run("port out of range rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{Host: "db", Port: 70000}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})

	t.Run("port negative rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{Host: "db", Port: -1}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of range")
	})

	t.Run("relative socket path rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{Socket: "run/dolt.sock"}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "is not absolute")
	})

	t.Run("tls cert without key rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{Host: "db", Port: 3306, TLSCert: "/etc/beads/client.pem"}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TLSCert set without TLSKey")
	})

	t.Run("tls key without cert rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{Host: "db", Port: 3306, TLSKey: "/etc/beads/client.key"}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TLSKey set without TLSCert")
	})

	t.Run("relative tls cert rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{
			Host:    "db",
			Port:    3306,
			TLSCert: "client.pem",
			TLSKey:  "/etc/beads/client.key",
		}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TLSCert")
		assert.Contains(t, err.Error(), "is not absolute")
	})

	t.Run("relative tls key rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{
			Host:    "db",
			Port:    3306,
			TLSCert: "/etc/beads/client.pem",
			TLSKey:  "client.key",
		}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TLSKey")
		assert.Contains(t, err.Error(), "is not absolute")
	})

	t.Run("negative keep alive period rejected", func(t *testing.T) {
		err := configfile.ExternalDoltConfig{Host: "db", Port: 3306, KeepAlivePeriod: -1 * time.Second}.Validate()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "KeepAlivePeriod")
		assert.Contains(t, err.Error(), "negative")
	})
}

func TestNewExternalDoltServer_RejectsInvalidConfig(t *testing.T) {
	_, err := NewExternalDoltServer(configfile.ExternalDoltConfig{})
	require.Error(t, err)
}

func TestExternalDoltServer_ID(t *testing.T) {
	t.Run("same tcp endpoint produces same id", func(t *testing.T) {
		a, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db", Port: 3306})
		require.NoError(t, err)
		b, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db", Port: 3306})
		require.NoError(t, err)
		assert.Equal(t, a.ID(context.Background()), b.ID(context.Background()))
	})

	t.Run("different ports produce different ids", func(t *testing.T) {
		a, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db", Port: 3306})
		require.NoError(t, err)
		b, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db", Port: 3307})
		require.NoError(t, err)
		assert.NotEqual(t, a.ID(context.Background()), b.ID(context.Background()))
	})

	t.Run("different hosts produce different ids", func(t *testing.T) {
		a, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db-a", Port: 3306})
		require.NoError(t, err)
		b, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db-b", Port: 3306})
		require.NoError(t, err)
		assert.NotEqual(t, a.ID(context.Background()), b.ID(context.Background()))
	})

	t.Run("socket and tcp produce different ids even when notation overlaps", func(t *testing.T) {
		a, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db", Port: 3306})
		require.NoError(t, err)
		b, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Socket: "/var/run/dolt.sock"})
		require.NoError(t, err)
		assert.NotEqual(t, a.ID(context.Background()), b.ID(context.Background()))
	})
}

func TestExternalDoltServerID_PureFunctionMatchesServer(t *testing.T) {
	cfg := configfile.ExternalDoltConfig{Host: "db", Port: 3306}
	srv, err := NewExternalDoltServer(cfg)
	require.NoError(t, err)
	assert.Equal(t, srv.ID(context.Background()), ExternalDoltServerID(cfg))
}

func TestExternalDoltServerID_AuthFieldsDoNotChangeID(t *testing.T) {
	base := configfile.ExternalDoltConfig{Host: "db", Port: 3306}
	withAuth := base
	withAuth.User = "beads"
	withAuth.TLSRequired = true
	withAuth.TLSCert = "/etc/beads/client.pem"
	withAuth.TLSKey = "/etc/beads/client.key"
	assert.Equal(t, ExternalDoltServerID(base), ExternalDoltServerID(withAuth))
}

func TestExternalDoltServer_DSN(t *testing.T) {
	t.Run("tcp without tls", func(t *testing.T) {
		s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db", Port: 3306})
		require.NoError(t, err)
		dsn := s.DSN(context.Background(), "beads", "root", "secret")
		assert.Contains(t, dsn, "tcp(db:3306)")
		assert.Contains(t, dsn, "/beads")
		assert.Contains(t, dsn, "tls=false")
	})

	t.Run("tls terminates client-side, not in this dsn", func(t *testing.T) {
		s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db", Port: 3306, TLSRequired: true})
		require.NoError(t, err)
		dsn := s.DSN(context.Background(), "beads", "root", "")
		assert.Contains(t, dsn, "tls=false")
	})

	t.Run("unix socket", func(t *testing.T) {
		s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Socket: "/var/run/dolt.sock"})
		require.NoError(t, err)
		dsn := s.DSN(context.Background(), "beads", "root", "")
		assert.Contains(t, dsn, "unix(/var/run/dolt.sock)")
		assert.NotContains(t, dsn, "tcp(")
	})

	t.Run("password embedded", func(t *testing.T) {
		s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db", Port: 3306})
		require.NoError(t, err)
		dsn := s.DSN(context.Background(), "beads", "u", "p@ss")
		assert.True(t, strings.HasPrefix(dsn, "u:p@ss@") || strings.Contains(dsn, "u:p%40ss@"))
	})
}

func TestExternalDoltServer_Lifecycle(t *testing.T) {
	s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db", Port: 3306})
	require.NoError(t, err)

	ctx := context.Background()
	assert.False(t, s.Running(ctx))

	require.NoError(t, s.Start(ctx))
	assert.True(t, s.Running(ctx))

	require.Error(t, s.Start(ctx), "double start should fail")

	require.NoError(t, s.Stop(ctx))
	assert.False(t, s.Running(ctx))

	require.NoError(t, s.Start(ctx), "restart after stop should work")
	assert.True(t, s.Running(ctx))
}

func TestExternalDoltServer_Dial(t *testing.T) {
	t.Run("tcp dial against a live listener succeeds", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		t.Cleanup(func() { _ = ln.Close() })

		host, portStr, err := net.SplitHostPort(ln.Addr().String())
		require.NoError(t, err)
		port, err := net.LookupPort("tcp", portStr)
		require.NoError(t, err)

		go func() {
			c, aerr := ln.Accept()
			if aerr == nil {
				_ = c.Close()
			}
		}()

		s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: host, Port: port})
		require.NoError(t, err)
		require.NoError(t, s.Start(context.Background()))
		t.Cleanup(func() { _ = s.Stop(context.Background()) })

		conn, err := s.Dial(context.Background())
		require.NoError(t, err)
		require.NotNil(t, conn)
		_ = conn.Close()
	})

	t.Run("tcp dial against a closed listener returns wrapped error", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		addr := ln.Addr().(*net.TCPAddr)
		require.NoError(t, ln.Close())

		s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "127.0.0.1", Port: addr.Port})
		require.NoError(t, err)
		require.NoError(t, s.Start(context.Background()))
		t.Cleanup(func() { _ = s.Stop(context.Background()) })

		_, err = s.Dial(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ExternalDoltServer.Dial")
	})

	t.Run("dial honors a canceled context", func(t *testing.T) {
		s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "10.255.255.1", Port: 9999})
		require.NoError(t, err)
		require.NoError(t, s.Start(context.Background()))
		t.Cleanup(func() { _ = s.Stop(context.Background()) })

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = s.Dial(ctx)
		require.Error(t, err)
	})

	t.Run("unix socket dial against a live listener succeeds", func(t *testing.T) {
		sockPath := filepath.Join(t.TempDir(), "dolt.sock")
		if len(sockPath) >= 104 {
			t.Skipf("socket path too long (%d bytes): %s", len(sockPath), sockPath)
		}
		ln, err := net.Listen("unix", sockPath)
		require.NoError(t, err)
		t.Cleanup(func() { _ = ln.Close() })

		go func() {
			c, aerr := ln.Accept()
			if aerr == nil {
				_ = c.Close()
			}
		}()

		s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Socket: sockPath})
		require.NoError(t, err)
		require.NoError(t, s.Start(context.Background()))
		t.Cleanup(func() { _ = s.Stop(context.Background()) })

		conn, err := s.Dial(context.Background())
		require.NoError(t, err)
		require.NotNil(t, conn)
		assert.Equal(t, "unix", conn.RemoteAddr().Network())
		_ = conn.Close()
	})

	t.Run("unix socket dial against a missing path returns wrapped error", func(t *testing.T) {
		s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{
			Socket: filepath.Join(t.TempDir(), "does-not-exist.sock"),
		})
		require.NoError(t, err)
		require.NoError(t, s.Start(context.Background()))
		t.Cleanup(func() { _ = s.Stop(context.Background()) })

		_, err = s.Dial(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ExternalDoltServer.Dial")
	})
}

func TestExternalDoltServer_StopIsIdempotent(t *testing.T) {
	s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db", Port: 3306})
	require.NoError(t, err)
	ctx := context.Background()

	require.NoError(t, s.Stop(ctx), "Stop before Start should be a no-op")
	require.NoError(t, s.Start(ctx))
	require.NoError(t, s.Stop(ctx))
	require.NoError(t, s.Stop(ctx), "double Stop should be a no-op")
	assert.False(t, s.Running(ctx))
}

func TestExternalDoltServer_DefaultKeepAlivePeriod(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	addr := ln.Addr().(*net.TCPAddr)

	go func() {
		c, aerr := ln.Accept()
		if aerr == nil {
			_ = c.Close()
		}
	}()

	s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{
		Host: "127.0.0.1",
		Port: addr.Port,
	})
	require.NoError(t, err)
	assert.Equal(t, defaultKeepAlivePeriod, s.keepAlivePeriod, "zero KeepAlivePeriod should default to the package default")

	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	conn, err := s.Dial(context.Background())
	require.NoError(t, err)
	require.NotNil(t, conn)
	_ = conn.Close()
}

func TestExternalDoltServer_CustomKeepAlivePeriodHonored(t *testing.T) {
	s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{
		Host:            "db",
		Port:            3306,
		KeepAlivePeriod: 7 * time.Second,
	})
	require.NoError(t, err)
	assert.Equal(t, 7*time.Second, s.keepAlivePeriod)
}

func TestExternalDoltServer_ConcurrentStart_SameInstance_OneWins(t *testing.T) {
	s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{Host: "db", Port: 3306})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Stop(context.Background()) })

	const n = 16
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = s.Start(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	winner := -1
	losers := 0
	for i, e := range errs {
		if e == nil {
			require.Equal(t, -1, winner, "more than one Start succeeded (%d and %d)", winner, i)
			winner = i
			continue
		}
		assert.Contains(t, e.Error(), "already started", "loser %d had unexpected error", i)
		losers++
	}
	require.GreaterOrEqual(t, winner, 0, "no Start succeeded")
	assert.Equal(t, n-1, losers)
	assert.True(t, s.Running(context.Background()))
}

func TestExternalDoltServer_MultipleInstances_SameEndpoint_AllStart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().(*net.TCPAddr)

	const n = 8
	servers := make([]*ExternalDoltServer, n)
	for i := 0; i < n; i++ {
		s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{
			Host: "127.0.0.1",
			Port: addr.Port,
		})
		require.NoError(t, err)
		servers[i] = s
	}
	t.Cleanup(func() {
		for _, s := range servers {
			_ = s.Stop(context.Background())
		}
	})

	first := servers[0].ID(context.Background())
	for i := 1; i < n; i++ {
		assert.Equal(t, first, servers[i].ID(context.Background()), "same endpoint must yield same ID across instances")
	}

	errs := make([]error, n)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			errs[i] = servers[i].Start(context.Background())
		}(i)
	}
	close(gate)
	wg.Wait()

	for i, e := range errs {
		require.NoError(t, e, "instance %d Start failed; external backend must not serialize across instances", i)
		assert.True(t, servers[i].Running(context.Background()))
	}
}

func TestExternalDoltServer_ConcurrentDial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	addr := ln.Addr().(*net.TCPAddr)

	acceptDone := make(chan struct{})
	go func() {
		defer close(acceptDone)
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			_ = c.Close()
		}
	}()

	s, err := NewExternalDoltServer(configfile.ExternalDoltConfig{
		Host: "127.0.0.1",
		Port: addr.Port,
	})
	require.NoError(t, err)
	require.NoError(t, s.Start(context.Background()))
	t.Cleanup(func() {
		_ = s.Stop(context.Background())
		_ = ln.Close()
		<-acceptDone
	})

	const n = 32
	errs := make([]error, n)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			conn, derr := s.Dial(ctx)
			if derr != nil {
				errs[i] = derr
				return
			}
			_ = conn.Close()
		}(i)
	}
	close(gate)
	wg.Wait()

	for i, e := range errs {
		require.NoError(t, e, "concurrent Dial %d failed", i)
	}
}
