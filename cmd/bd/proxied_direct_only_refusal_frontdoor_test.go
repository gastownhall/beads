//go:build cgo && unix

package main

import (
	"strings"
	"testing"
	"time"
)

func TestProxiedDirectOnlyRefusalsFrontDoor(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "directonly", 5*time.Minute)
	for _, args := range [][]string{{"sync"}, {"dolt", "remote", "add"}, {"dolt", "remote", "list"}, {"dolt", "remote", "reset-data"}, {"dolt", "push"}, {"dolt", "pull"}, {"dolt", "commit"}} {
		t.Run(strings.Join(args, "/"), func(t *testing.T) {
			out, errOut, err := bdProxiedRunBuffers(t, bd, p.dir, append(args, "--json")...)
			if err == nil {
				t.Fatal("expected refusal")
			}
			if !strings.Contains(strings.ToLower(out+errOut), "unsupported") && !strings.Contains(strings.ToLower(out+errOut), "proxied") {
				t.Fatalf("unexpected refusal: %s%s", out, errOut)
			}
		})
	}
}
