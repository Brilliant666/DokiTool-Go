//go:build unix

// Adapted from wader/goutubedl and lrstanley/go-ytdlp (MIT License).
package ytengine

import (
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err == nil {
			return nil
		}
		return cmd.Process.Kill()
	}
	cmd.WaitDelay = 2 * time.Second
}

func dirOwnedByUID(info os.FileInfo, expectedUID int) bool {
	if expectedUID < 0 || info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return false
	}
	return int(stat.Uid) == expectedUID
}
