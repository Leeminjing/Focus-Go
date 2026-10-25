package workspace

import (
	"fmt"
	"os"
	"syscall"
)

func identity(f *os.File, info os.FileInfo) (string, uint32, error) {
	var data syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &data); err != nil {
		return "", 0, err
	}
	return fmt.Sprintf("win-%08x-%08x%08x", data.VolumeSerialNumber, data.FileIndexHigh, data.FileIndexLow), data.NumberOfLinks, nil
}
func linked(info os.FileInfo) bool {
	if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return true
	}
	if data, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return true
}
