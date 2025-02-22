/*
   Copyright The Soci Snapshotter Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

/*
   Copyright The containerd Authors.

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

// =====
// NOTE: This file is ported from https://github.com/containerd/containerd/blob/v1.5.2/snapshots/overlay/overlayutils/check.go
// TODO: import this from containerd package once we drop support to continerd v1.4.x
// =====

package overlayutils

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/containerd/containerd/log"
	"github.com/containerd/containerd/mount"
	userns "github.com/containerd/containerd/sys"
	"github.com/containerd/continuity/fs"
	"github.com/pkg/errors"
)

// SupportsMultipleLowerDir checks if the system supports multiple lowerdirs,
// which is required for the overlay snapshotter. On 4.x kernels, multiple lowerdirs
// are always available (so this check isn't needed), and backported to RHEL and
// CentOS 3.x kernels (3.10.0-693.el7.x86_64 and up). This function is to detect
// support on those kernels, without doing a kernel version compare.
//
// Ported from moby overlay2.
func SupportsMultipleLowerDir(d string) error {
	td, err := os.MkdirTemp(d, "multiple-lowerdir-check")
	if err != nil {
		return err
	}
	defer func() {
		if err := os.RemoveAll(td); err != nil {
			log.L.WithError(err).Warnf("Failed to remove check directory %v", td)
		}
	}()

	for _, dir := range []string{"lower1", "lower2", "upper", "work", "merged"} {
		if err := os.Mkdir(filepath.Join(td, dir), 0755); err != nil {
			return err
		}
	}

	opts := fmt.Sprintf("lowerdir=%s:%s,upperdir=%s,workdir=%s", filepath.Join(td, "lower2"), filepath.Join(td, "lower1"), filepath.Join(td, "upper"), filepath.Join(td, "work"))
	m := mount.Mount{
		Type:    "overlay",
		Source:  "overlay",
		Options: []string{opts},
	}
	dest := filepath.Join(td, "merged")
	if err := m.Mount(dest); err != nil {
		return errors.Wrap(err, "failed to mount overlay")
	}
	if err := mount.UnmountAll(dest, 0); err != nil {
		log.L.WithError(err).Warnf("Failed to unmount check directory %v", dest)
	}
	return nil
}

// Supported returns nil when the overlayfs is functional on the system with the root directory.
// Supported is not called during plugin initialization, but exposed for downstream projects which uses
// this snapshotter as a library.
func Supported(root string) error {
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	supportsDType, err := fs.SupportsDType(root)
	if err != nil {
		return err
	}
	if !supportsDType {
		return fmt.Errorf("%s does not support d_type. If the backing filesystem is xfs, please reformat with ftype=1 to enable d_type support", root)
	}
	return SupportsMultipleLowerDir(root)
}

// NeedsUserXAttr returns whether overlayfs should be mounted with the "userxattr" mount option.
//
// The "userxattr" option is needed for mounting overlayfs inside a user namespace with kernel >= 5.11.
//
// The "userxattr" option is NOT needed for the initial user namespace (aka "the host").
//
// Also, Ubuntu (since circa 2015) and Debian (since 10) with kernel < 5.11 can mount
// the overlayfs in a user namespace without the "userxattr" option.
//
// The corresponding kernel commit: https://github.com/torvalds/linux/commit/2d2f2d7322ff43e0fe92bf8cccdc0b09449bf2e1
// > ovl: user xattr
// >
// > Optionally allow using "user.overlay." namespace instead of "trusted.overlay."
// > ...
// > Disable redirect_dir and metacopy options, because these would allow privilege escalation through direct manipulation of the
// > "user.overlay.redirect" or "user.overlay.metacopy" xattrs.
// > ...
//
// The "userxattr" support is not exposed in "/sys/module/overlay/parameters".
func NeedsUserXAttr(d string) (bool, error) {
	if !userns.RunningInUserNS() {
		// we are the real root (i.e., the root in the initial user NS),
		// so we do never need "userxattr" opt.
		return false, nil
	}

	// TODO: add fast path for kernel >= 5.11 .
	//
	// Keep in mind that distro vendors might be going to backport the patch to older kernels.
	// So we can't completely remove the check.

	tdRoot := filepath.Join(d, "userxattr-check")
	if err := os.RemoveAll(tdRoot); err != nil {
		log.L.WithError(err).Warnf("Failed to remove check directory %v", tdRoot)
	}

	if err := os.MkdirAll(tdRoot, 0700); err != nil {
		return false, err
	}

	defer func() {
		if err := os.RemoveAll(tdRoot); err != nil {
			log.L.WithError(err).Warnf("Failed to remove check directory %v", tdRoot)
		}
	}()

	td, err := os.MkdirTemp(tdRoot, "")
	if err != nil {
		return false, err
	}

	for _, dir := range []string{"lower1", "lower2", "upper", "work", "merged"} {
		if err := os.Mkdir(filepath.Join(td, dir), 0755); err != nil {
			return false, err
		}
	}

	opts := []string{
		fmt.Sprintf("lowerdir=%s:%s,upperdir=%s,workdir=%s", filepath.Join(td, "lower2"), filepath.Join(td, "lower1"), filepath.Join(td, "upper"), filepath.Join(td, "work")),
		"userxattr",
	}

	m := mount.Mount{
		Type:    "overlay",
		Source:  "overlay",
		Options: opts,
	}

	dest := filepath.Join(td, "merged")
	if err := m.Mount(dest); err != nil {
		// Probably the host is running Ubuntu/Debian kernel (< 5.11) with the userns patch but without the userxattr patch.
		// Return false without error.
		log.L.WithError(err).Debugf("cannot mount overlay with \"userxattr\", probably the kernel does not support userxattr")
		return false, nil
	}
	if err := mount.UnmountAll(dest, 0); err != nil {
		log.L.WithError(err).Warnf("Failed to unmount check directory %v", dest)
	}
	return true, nil
}
