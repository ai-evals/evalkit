package engine

import (
	"errors"
	"io"
	"os"
)

const maxEvidenceFileBytes int64 = 16 << 20

// readEvidenceFile rejects symlinks and nonregular inputs and limits size.
// This is a basic safe reader; it does not promise protection against a
// hostile actor concurrently swapping ancestor directories on the host.
func readEvidenceFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxEvidenceFileBytes {
		return nil, errors.New("artifact evidence file must be a bounded regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, errors.New("artifact file changed during open")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxEvidenceFileBytes+1))
	if err != nil || int64(len(b)) > maxEvidenceFileBytes {
		return nil, errors.New("artifact evidence exceeds read limit")
	}
	return b, nil
}
