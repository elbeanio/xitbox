//go:build linux

package sandbox

import (
	"fmt"
	"os/exec"

	"github.com/iangeorge/xitbox/pkg/config"
)

// cgroupsWrap optionally wraps cmd inside a systemd transient user scope so
// that MemoryMax, CPUQuota, and TasksMax are enforced via cgroup v2.
//
// Returns cmd unchanged if:
//   - systemd-run is not in PATH (no systemd session)
//   - all resource limits are unset (zero/empty)
//
// The wrapped command is equivalent to:
//
//	systemd-run --scope --user --quiet --collect \
//	  --property=MemoryMax=<memory> \
//	  --property=CPUQuota=<cpus*100>% \
//	  --property=TasksMax=<pids> \
//	  -- <original cmd and args>
func cgroupsWrap(cmd *exec.Cmd, cfg *config.Config) *exec.Cmd {
	if _, err := exec.LookPath("systemd-run"); err != nil {
		return cmd
	}
	r := cfg.Resources
	if r.Memory == "" && r.CPUs == 0 && r.PIDs == 0 {
		return cmd
	}

	sdArgs := []string{"--scope", "--user", "--quiet", "--collect"}
	if r.Memory != "" {
		sdArgs = append(sdArgs, "--property=MemoryMax="+r.Memory)
	}
	if r.CPUs > 0 {
		sdArgs = append(sdArgs, fmt.Sprintf("--property=CPUQuota=%d%%", r.CPUs*100))
	}
	if r.PIDs > 0 {
		sdArgs = append(sdArgs, fmt.Sprintf("--property=TasksMax=%d", r.PIDs))
	}
	sdArgs = append(sdArgs, "--", cmd.Path)
	sdArgs = append(sdArgs, cmd.Args[1:]...)

	wrapped := exec.Command("systemd-run", sdArgs...)
	wrapped.Stdin = cmd.Stdin
	wrapped.Stdout = cmd.Stdout
	wrapped.Stderr = cmd.Stderr
	wrapped.Env = cmd.Env
	return wrapped
}
