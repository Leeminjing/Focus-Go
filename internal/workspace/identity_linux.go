package workspace

import (
	"fmt"
	"os"
	"syscall"
)

func readFlags() int { return os.O_RDONLY | syscall.O_NONBLOCK }

func identity(f *os.File, info os.FileInfo) (string, uint32, error) {
	data, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", 0, ErrDenied
	}
	return fmt.Sprintf("unix-%x-%x", data.Dev, data.Ino), uint32(data.Nlink), nil
}
func linked(info os.FileInfo) bool { return info.Mode()&os.ModeSymlink != 0 }
