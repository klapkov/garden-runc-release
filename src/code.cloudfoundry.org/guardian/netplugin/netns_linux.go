//go:build linux

package netplugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

// createBoundNetns creates a new network namespace and bind-mounts it at path,
// so it outlives the thread that created it and can be handed to a runtime
// (via the OCI spec) and to the external networker (as an inherited fd).
func createBoundNetns(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating netns dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_RDONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return fmt.Errorf("creating netns mountpoint: %w", err)
	}
	f.Close()

	errCh := make(chan error, 1)
	go func() {
		// The thread switches into the new namespace and is never unlocked,
		// so the Go runtime terminates it when this goroutine exits instead of
		// reusing a thread that is in the wrong network namespace.
		runtime.LockOSThread()

		if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
			errCh <- fmt.Errorf("unshare netns: %w", err)
			return
		}

		src := fmt.Sprintf("/proc/self/task/%d/ns/net", unix.Gettid())
		if err := unix.Mount(src, path, "none", unix.MS_BIND, ""); err != nil {
			errCh <- fmt.Errorf("bind-mounting netns: %w", err)
			return
		}
		errCh <- nil
	}()

	if err := <-errCh; err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

// removeBoundNetns unmounts and deletes a namespace created by createBoundNetns.
// It is idempotent.
func removeBoundNetns(path string) error {
	if err := unix.Unmount(path, unix.MNT_DETACH); err != nil && !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("unmounting netns %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing netns %s: %w", path, err)
	}
	return nil
}
