//go:build !windows && !linux

package workspace

import "os"

func identity(*os.File, os.FileInfo) (string, uint32, error) { return "", 0, ErrDenied }
func linked(os.FileInfo) bool                                { return true }
