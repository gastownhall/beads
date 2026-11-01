package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpclient/wire"
)

func TestResponseHeaderTimeoutResolution(t *testing.T) {
	cases := []struct {
		opt     time.Duration
		env     string
		want    time.Duration
		wantErr bool
	}{
		{opt: 0, env: "", want: 0}, // wire's default
		{opt: 0, env: "5s", want: 5 * time.Second},
		{opt: 0, env: "30", want: 30 * time.Second}, // seconds, as BEADS_GATE_WAIT_TIMEOUT takes
		{opt: 0, env: " 1.5 ", want: 1500 * time.Millisecond},
		{opt: 0, env: "off", want: -1},
		{opt: 0, env: "OFF", want: -1},
		{opt: 0, env: "0", want: -1},
		{opt: 0, env: "0s", want: -1},
		{opt: 0, env: "soon", wantErr: true},
		{opt: 0, env: "-3s", wantErr: true},
		{opt: 0, env: "-3", wantErr: true},
		{opt: 2 * time.Second, env: "5s", want: 2 * time.Second}, // the option wins
	}
	for _, tc := range cases {
		t.Setenv(ResponseHeaderTimeoutEnv, tc.env)
		got, err := responseHeaderTimeout(tc.opt)
		if (err != nil) != tc.wantErr {
			t.Errorf("opt=%v env=%q: err = %v, wantErr %v", tc.opt, tc.env, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got != tc.want {
			t.Errorf("opt=%v env=%q: got %v, want %v", tc.opt, tc.env, got, tc.want)
		}
	}
}

func TestAnUnparseableHeaderTimeoutEnvRefusesTheDial(t *testing.T) {
	clearCAEnvironment(t)
	t.Setenv(ResponseHeaderTimeoutEnv, "soon")
	if _, err := Dial(Target{BaseURL: mustParseURL(t, "http://127.0.0.1:1")}, DialOptions{}); err == nil {
		t.Fatal("Dial succeeded with an unparseable header timeout")
	}
}

// TestTheProductionDialRetriesAStalledReadOnAnotherConnection is the shadow
// run's stall end to end through the production transport DialWith builds:
// the first connection never answers, the read is retried once on another one
// and succeeds within about the header timeout, not the 60s overall timeout.
func TestTheProductionDialRetriesAStalledReadOnAnotherConnection(t *testing.T) {
	clearCAEnvironment(t)
	t.Setenv(ResponseHeaderTimeoutEnv, "300ms")
	var mu sync.Mutex
	first := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if first == "" {
			first = r.RemoteAddr
		}
		stuck := r.RemoteAddr == first
		mu.Unlock()
		if stuck {
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	var events []wire.StallRetry
	conn, err := Dial(Target{BaseURL: mustParseURL(t, srv.URL)}, DialOptions{
		OnStallRetry: func(e wire.StallRetry) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	start := time.Now()
	if err := conn.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("elapsed %v, want about one 300ms header timeout", elapsed)
	}
	if len(events) != 1 || events[0].Retry != 1 {
		t.Fatalf("events = %+v, want exactly one retry", events)
	}
}
