// Copyright (c) 2018 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0
//

package utils

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// from <linux/vhost.h>
// VHOST_VSOCK_SET_GUEST_CID = _IOW(VHOST_VIRTIO, 0x60, __u64)
const ioctlVhostVsockSetGuestCid = 0x4008AF60

var ioctlFunc = Ioctl

// maxUInt represents the maximum valid value for the context ID.
// The upper 32 bits of the CID are reserved and zeroed.
// See http://stefanha.github.io/virtio/
var maxUInt uint64 = 1<<32 - 1

func Ioctl(fd uintptr, request, data uintptr) error {
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, request, data); errno != 0 {
		//uintptr(request)
		//uintptr(unsafe.Pointer(&arg1)),
		//); errno != 0 {
		return os.NewSyscallError("ioctl", fmt.Errorf("%d", int(errno)))
	}

	return nil
}

// FindContextID finds a unique context ID by generating a random number between 3 and max unsigned int (maxUint).
// Using the ioctl VHOST_VSOCK_SET_GUEST_CID, findContextID asks to the kernel if the given
// context ID (N) is available, when the context ID is not available, incrementing by 1 findContextID
// iterates from N to maxUint until an available context ID is found, otherwise decrementing by 1
// findContextID iterates from N to 3 until an available context ID is found, this is the last chance
// to find a context ID available.
// On success vhost file and a context ID greater or equal than 3 are returned, otherwise 0 and an error are returned.
// vhost file can be used to send vhost file decriptor to QEMU. It's the caller's responsibility to
// close vhost file descriptor.
//
// Benefits of using random context IDs:
// - Reduce the probability of a *DoS attack*, since other processes don't know whatis the initial context ID
//   used by findContextID to find a context ID available
//
func FindContextID() (*os.File, uint64, error) {
	// context IDs 0x0, 0x1 and 0x2 are reserved, 0x3 is the first context ID usable.
	var firstContextID uint64 = 0x3
	var contextID = firstContextID

	// Generate a random number
	n, err := rand.Int(rand.Reader, big.NewInt(int64(maxUInt)))
	if err == nil && n.Int64() >= int64(firstContextID) {
		contextID = uint64(n.Int64())
	}

	// Open vhost-vsock device to check what context ID is available.
	// This file descriptor holds/locks the context ID and it should be
	// inherited by QEMU process.
	vsockFd, err := os.OpenFile(VHostVSockDevicePath, syscall.O_RDWR, 0666)
	if err != nil {
		return nil, 0, err
	}

	// Looking for the first available context ID.
	for cid := contextID; cid <= maxUInt; cid++ {
		if err := ioctlFunc(vsockFd.Fd(), ioctlVhostVsockSetGuestCid, uintptr(unsafe.Pointer(&cid))); err == nil {
			return vsockFd, cid, nil
		}
	}

	// Last chance to get a free context ID.
	for cid := contextID - 1; cid >= firstContextID; cid-- {
		if err := ioctlFunc(vsockFd.Fd(), ioctlVhostVsockSetGuestCid, uintptr(unsafe.Pointer(&cid))); err == nil {
			return vsockFd, cid, nil
		}
	}

	vsockFd.Close()
	return nil, 0, fmt.Errorf("Could not get a unique context ID for the vsock")
}

func GetDevFormat(disk string) (string, error) {
	// refer to https://github.com/kubernetes/kubernetes/blob/v1.12.2/pkg/util/mount/mount_linux.go#L512
	args := []string{"-p", "-s", "TYPE", "-s", "PTTYPE", "-o", "export", disk}
	dataOut, err := exec.Command("blkid", args...).Output()
	output := string(dataOut)

	if err != nil {
		if strings.Contains(err.Error(), "exit status 2") {
			// Disk device is unformatted.
			// For `blkid`, if the specified token (TYPE/PTTYPE, etc) was
			// not found, or no (specified) devices could be identified, an
			// exit code of 2 is returned.
			return "", nil
		}
		return "", err
	}

	var fstype string

	lines := strings.Split(output, "\n")
	for _, l := range lines {
		if len(l) <= 0 {
			// Ignore empty line.
			continue
		}
		// if we use busybox as rootfs,the output of command
		// `busybox blkid` is different with original`blkid`,
		// so we should make a compatible parse
		subLine := strings.Split(l, " ")
		for _, sl := range subLine {
			if len(subLine) <= 0 {
				continue
			}
			cs := strings.Split(sl, "=")
			if len(cs) != 2 {
				continue
			}
			if cs[0] == "TYPE" {
				fstype = cs[1]
				if strings.Contains(fstype, `"`) {
					fstype = strings.Replace(fstype, `"`, "", -1)
				}
			}
		}
	}

	return fstype, nil
}
