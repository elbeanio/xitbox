//go:build !linux

package sandbox

import (
	"fmt"

	"github.com/iangeorge/xitbox/pkg/config"
)

func runLinux(rt *Runtime, cfg *config.Config, command []string, guardianPort string) error {
	return fmt.Errorf("linux backend called on non-linux platform")
}
