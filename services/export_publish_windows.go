package services

import (
	"os"

	"golang.org/x/sys/windows"
)

func publishModDirectory(temp, dest string) error {
	from, err := windows.UTF16PtrFromString(temp)
	if err != nil {
		return &os.LinkError{Op: "rename", Old: temp, New: dest, Err: err}
	}
	to, err := windows.UTF16PtrFromString(dest)
	if err == nil {
		// os.Rename enables replacement on Windows; publishing must never overwrite.
		err = windows.MoveFileEx(from, to, 0)
	}
	if err != nil {
		return &os.LinkError{Op: "rename", Old: temp, New: dest, Err: err}
	}
	return nil
}
