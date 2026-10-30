//go:build linux || darwin

package engine

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"time"
)

// runProcessGroup gives each command its own process group so cancellation
// targets descendants as well as the direct child. This is best-effort
// containment, NOT a filesystem/network sandbox: an untrusted process may
// create a new session and escape its group.
func runProcessGroup(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	case <-ctx.Done():
		// SIGKILL is used only after the authoritative deadline/cancellation
		// request, never for normal completion.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err != nil && !errors.Is(err, syscall.ESRCH) {
			return errors.New("process group termination could not be requested")
		}
		select {
		case <-done:
			return ctx.Err()
		case <-time.After(2 * time.Second):
			return errors.New("process-group cleanup remains unconfirmed")
		}
	}
}
