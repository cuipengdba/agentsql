//go:build windows

package b5wal

import "os"

// SyncDirectory verifies the directory is accessible. On Windows, NTFS journals
// directory metadata (create/rename/unlink), and FlushFileBuffers on a directory
// handle requires SE_MANAGE_VOLUME_NAME, otherwise it fails with
// ERROR_ACCESS_DENIED. Durable write guarantees are still provided by file-level
// Sync (FlushFileBuffers), which is used for every segment, manifest, key and
// receipt.
func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return directory.Close()
}
