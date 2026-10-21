//go:build windows

package extract

import (
	"context"
	"syscall"
	"testing"
)

func TestIsProjectFailureWindowsConnectionReset(t *testing.T) {
	if !isProjectFailure(context.Background(), netOpError(syscall.WSAECONNRESET)) {
		t.Fatal("expected Windows connection reset to be a project failure")
	}
}
