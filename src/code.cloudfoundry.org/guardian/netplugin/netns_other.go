//go:build !linux

package netplugin

import "errors"

func createBoundNetns(path string) error {
	return errors.New("creating a network namespace before the container is only supported on linux")
}

func flattenPeerAddresses(nsPath string) error {
	return nil
}

func removeBoundNetns(path string) error {
	return nil
}
