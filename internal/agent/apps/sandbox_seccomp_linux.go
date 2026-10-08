//go:build linux && (amd64 || arm64)

package apps

import (
	"bytes"
	"encoding/binary"
	"golang.org/x/sys/unix"
	"os"
	"runtime"
)

// Deny namespace creation after entry, host-process inspection, and alternate
// kernel I/O APIs. clone3 returns ENOSYS so libc can use clone for normal threads.
func appSeccompFile() (*os.File, error) {
	arch := uint32(unix.AUDIT_ARCH_X86_64)
	if runtime.GOARCH == "arm64" {
		arch = unix.AUDIT_ARCH_AARCH64
	}
	load := func(offset uint32) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: offset}
	}
	ret := func(value uint32) unix.SockFilter { return unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: value} }
	eq := func(value uint32, yes, no uint8) unix.SockFilter {
		return unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: value, Jt: yes, Jf: no}
	}
	deny := uint32(unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM))
	filters := []unix.SockFilter{load(4), eq(arch, 1, 0), ret(unix.SECCOMP_RET_KILL_PROCESS), load(0)}
	if runtime.GOARCH == "amd64" {
		filters = append(filters, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JGE | unix.BPF_K, K: 0x40000000, Jf: 1}, ret(unix.SECCOMP_RET_KILL_PROCESS))
	}
	for _, call := range []uint32{unix.SYS_UNSHARE, unix.SYS_SETNS, unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_CHROOT, unix.SYS_PTRACE, unix.SYS_PROCESS_VM_READV, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_KCMP, unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_KEYCTL, unix.SYS_ADD_KEY, unix.SYS_REQUEST_KEY, unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN, unix.SYS_IO_URING_SETUP, unix.SYS_FSOPEN, unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_MOVE_MOUNT, unix.SYS_OPEN_TREE, unix.SYS_MOUNT_SETATTR, unix.SYS_PIDFD_GETFD} {
		filters = append(filters, eq(call, 0, 1), ret(deny))
	}
	filters = append(filters, eq(unix.SYS_CLONE3, 0, 1), ret(unix.SECCOMP_RET_ERRNO|uint32(unix.ENOSYS)))
	namespaceFlags := uint32(unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWNET | unix.CLONE_NEWPID | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS | unix.CLONE_NEWCGROUP)
	filters = append(filters, eq(unix.SYS_CLONE, 0, 3), load(16), unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: namespaceFlags, Jf: 1}, ret(deny), load(0))
	filters = append(filters, eq(unix.SYS_SOCKET, 0, 3), load(16), eq(unix.AF_UNIX, 1, 0), ret(deny), ret(unix.SECCOMP_RET_ALLOW))
	var data bytes.Buffer
	if err := binary.Write(&data, binary.LittleEndian, filters); err != nil {
		return nil, err
	}
	fd, err := unix.MemfdCreate("hank-app-seccomp", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "app-seccomp")
	if _, err = file.Write(data.Bytes()); err != nil {
		file.Close()
		return nil, err
	}
	if _, err = file.Seek(0, 0); err != nil {
		file.Close()
		return nil, err
	}
	if _, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, unix.F_SEAL_WRITE|unix.F_SEAL_GROW|unix.F_SEAL_SHRINK|unix.F_SEAL_SEAL); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}
