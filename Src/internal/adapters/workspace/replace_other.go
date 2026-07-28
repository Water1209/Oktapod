//go:build !windows

package workspace

import "os"

func atomicReplaceFile(source, target string) error {
	return os.Rename(source, target)
}
