package main

import "testing"

func TestCPUProfileFlagRegistered(t *testing.T) {
	flag := rootCmd.PersistentFlags().Lookup("cpu-profile")
	if flag == nil {
		t.Fatal("--cpu-profile persistent flag is not registered")
	}
	if got, want := flag.Value.Type(), "bool"; got != want {
		t.Errorf("--cpu-profile flag type = %q, want %q", got, want)
	}
	if got, want := flag.Usage, "Generate CPU profile for performance analysis"; got != want {
		t.Errorf("--cpu-profile usage = %q, want %q", got, want)
	}
	if old := rootCmd.PersistentFlags().Lookup("profile"); old != nil {
		t.Error("--profile must not remain registered as the CPU profiling flag")
	}
}
