// This controlled Git stand-in blocks a config write until the test releases it.
package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 7 || os.Args[1] != "--git-dir" || os.Args[3] != "config" || os.Args[4] != "--local" || os.Args[5] != "core.hooksPath" {
		fmt.Fprintln(os.Stderr, "unexpected Git operation", os.Args[1:])
		os.Exit(2)
	}
	conn, err := net.DialTimeout("tcp", os.Getenv("BEADS_HOOK_CONFIG_ADDR"), 5*time.Second)
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(30 * time.Second)); err != nil {
		panic(err)
	}
	if _, err := fmt.Fprintln(conn, "config ready"); err != nil {
		panic(err)
	}
	if _, err := io.ReadFull(conn, make([]byte, 1)); err != nil {
		panic(err)
	}
	if err := os.WriteFile(os.Getenv("BEADS_HOOK_CONFIG_WRITE"), []byte("configured"), 0600); err != nil {
		panic(err)
	}
}
