//go:build !windows

package b5wal

import (
	"errors"
	"os"
)

// SyncDirectory fsyncs a directory so that recently created, renamed or
// unlinked directory entries are durable.
func SyncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	err = directory.Sync()
	return errors.Join(err, directory.Close())
}
