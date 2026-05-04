//go:build !windows

package worker

import "syscall"

// extraOpenFlags adds O_NOFOLLOW on Unix so opening the active log file
// refuses to follow a symlink. Without this, an attacker who can create
// arbitrary files in ~/.justtunnel/logs (e.g. via a shared workstation
// mistake or a buggy installer) could plant a symlink pointing the worker
// at a system file (e.g. /etc/shadow on Linux, /var/log/system.log on
// macOS) and have the worker append log lines into that file.
//
// Windows does NOT have an O_NOFOLLOW equivalent (NTFS reparse points
// require a different API), so the windows variant returns 0 — see
// logfile_windows.go.
const extraOpenFlags = syscall.O_NOFOLLOW
