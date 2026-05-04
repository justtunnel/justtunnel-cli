//go:build windows

package worker

// extraOpenFlags is 0 on Windows: NTFS reparse points / symlinks need a
// CreateFile flag (FILE_FLAG_OPEN_REPARSE_POINT) that os.OpenFile does
// not surface. The worker's Windows-supported deployment mode is
// foreground via `worker start`; the symlink-attack threat model that
// motivated O_NOFOLLOW elsewhere is less acute here. See logfile_unix.go.
const extraOpenFlags = 0
