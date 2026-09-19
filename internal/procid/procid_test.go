//go:build !windows

package procid

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestCaptureAndVerifySelf(t *testing.T) {
	tok, err := Capture(os.Getpid())
	if err != nil {
		t.Fatalf("Capture(self): %v", err)
	}
	matched, err := Verify(os.Getpid(), tok)
	if err != nil {
		t.Fatalf("Verify(self): %v", err)
	}
	if !matched {
		t.Fatal("Verify(self) = false, want true")
	}
	if runtime.GOOS == "linux" {
		parts := strings.Split(string(tok), ":")
		if len(parts) != 3 || parts[0] != "linux-v1" {
			t.Fatalf("linux token = %q, want linux-v1 with three parts", tok)
		}
	}
}

func TestVerifyExitedChild(t *testing.T) {
	cmd := exec.Command("sleep", "0.05")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	tok, err := Capture(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Wait()
		t.Fatalf("Capture(child): %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait child: %v", err)
	}
	matched, err := Verify(cmd.Process.Pid, tok)
	if err != nil {
		t.Fatalf("Verify(exited child): %v", err)
	}
	if matched {
		t.Fatal("Verify(exited child) = true, want false")
	}
	// On Darwin this specifically exercises SysctlKinfoProc's zero-length/EIO
	// missing-pid result; Linux normally reports ENOENT from /proc/<pid>/stat.
}

func TestVerifyWrongToken(t *testing.T) {
	tok, err := Capture(os.Getpid())
	if err != nil {
		t.Fatalf("Capture(self): %v", err)
	}
	wrongText := string(tok)
	last := wrongText[len(wrongText)-1]
	if last == '0' {
		last = '1'
	} else {
		last = '0'
	}
	wrong := Token(wrongText[:len(wrongText)-1] + string(last))
	matched, err := Verify(os.Getpid(), wrong)
	if err != nil {
		t.Fatalf("Verify(wrong token): %v", err)
	}
	if matched {
		t.Fatal("Verify(wrong token) = true, want false")
	}
}

func TestOpen(t *testing.T) {
	tok, err := Capture(os.Getpid())
	if err != nil {
		t.Fatalf("Capture(self): %v", err)
	}
	h, err := Open(os.Getpid(), tok)
	if err != nil {
		t.Fatalf("Open(self): %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("Close(self): %v", err)
	}

	cmd := exec.Command("sleep", "0.05")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	childToken, err := Capture(cmd.Process.Pid)
	if err != nil {
		_ = cmd.Wait()
		t.Fatalf("Capture(child): %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait child: %v", err)
	}
	if _, err := Open(cmd.Process.Pid, childToken); err == nil {
		t.Fatal("Open(exited child) succeeded, want error")
	}
	_, err = Capture(cmd.Process.Pid)
	if err == nil {
		t.Fatal("Capture(exited child) succeeded, want error")
	}
	if !IsProcessGone(err) {
		t.Fatalf("IsProcessGone(Capture(exited child)) = false for %v", err)
	}
	// The Darwin assertion above covers both wrapped ESRCH and the EIO mapping
	// used by SysctlKinfoProc for a zero-length missing-pid result.
}

func TestCaptureNonexistentProcess(t *testing.T) {
	if _, err := Capture(1 << 22); err == nil {
		t.Fatal("Capture(nonexistent process) succeeded, want error")
	} else if !IsProcessGone(err) {
		t.Fatalf("IsProcessGone(Capture(nonexistent process)) = false for %v", err)
	}
}
