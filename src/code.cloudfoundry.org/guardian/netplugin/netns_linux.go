//go:build linux

package netplugin

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
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

// flattenPeerAddresses rewrites point-to-point addresses ("local peer P/32",
// as configured by silk) into plain local/32 addresses plus an explicit
// link-scope route to the peer, keeping the default route via the peer.
// Kata mirrors IFA_ADDRESS into the guest, which for peer addresses is the
// peer (gateway) IP, so the guest would otherwise send with the wrong source.
func flattenPeerAddresses(nsPath string) error {
	ns, err := netns.GetFromPath(nsPath)
	if err != nil {
		return fmt.Errorf("getting netns %s: %w", nsPath, err)
	}
	defer ns.Close()

	h, err := netlink.NewHandleAt(ns)
	if err != nil {
		return fmt.Errorf("netlink handle in %s: %w", nsPath, err)
	}
	defer h.Close()

	links, err := h.LinkList()
	if err != nil {
		return fmt.Errorf("listing links: %w", err)
	}

	for _, link := range links {
		addrs, err := h.AddrList(link, netlink.FAMILY_V4)
		if err != nil {
			return fmt.Errorf("listing addresses on %s: %w", link.Attrs().Name, err)
		}
		for _, addr := range addrs {
			if addr.Peer == nil || addr.Peer.IP.Equal(addr.IP) {
				continue
			}

			routes, err := h.RouteList(link, netlink.FAMILY_V4)
			if err != nil {
				return fmt.Errorf("listing routes on %s: %w", link.Attrs().Name, err)
			}
			// silk pins the gateway MAC with a permanent neighbor on a NOARP link.
			neighs, err := h.NeighList(link.Attrs().Index, netlink.FAMILY_V4)
			if err != nil {
				return fmt.Errorf("listing neighbors on %s: %w", link.Attrs().Name, err)
			}

			// Add the plain address before deleting the peer one: removing the
			// last IPv4 address flushes the link's neighbors, permanent included.
			local := &netlink.Addr{IPNet: &net.IPNet{IP: addr.IP, Mask: net.CIDRMask(32, 32)}}
			if err := h.AddrAdd(link, local); err != nil {
				return fmt.Errorf("adding address %s: %w", local, err)
			}
			if err := h.AddrDel(link, &addr); err != nil {
				return fmt.Errorf("deleting peer address %s: %w", addr, err)
			}

			peerRoute := &netlink.Route{
				LinkIndex: link.Attrs().Index,
				Dst:       &net.IPNet{IP: addr.Peer.IP, Mask: net.CIDRMask(32, 32)},
				Scope:     netlink.SCOPE_LINK,
				Src:       addr.IP,
			}
			if err := h.RouteReplace(peerRoute); err != nil {
				return fmt.Errorf("adding peer route via %s: %w", link.Attrs().Name, err)
			}

			for _, r := range routes {
				if r.Gw == nil || r.Protocol == unix.RTPROT_KERNEL {
					continue
				}
				r.Src = addr.IP
				if err := h.RouteReplace(&r); err != nil {
					return fmt.Errorf("restoring route %s: %w", r, err)
				}
			}

			for _, n := range neighs {
				if n.State&netlink.NUD_PERMANENT == 0 {
					continue
				}
				n := n
				if err := h.NeighSet(&n); err != nil {
					return fmt.Errorf("restoring neighbor %s: %w", n.IP, err)
				}
			}
		}
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
