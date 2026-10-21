//go:build !windows

package extract

import (
	"errors"
	"syscall"
)

func isConnReset(err error) bool {
	return errors.Is(err, syscall.ECONNRESET)
}
