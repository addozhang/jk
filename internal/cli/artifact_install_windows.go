//go:build windows

package cli

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32DLL = syscall.NewLazyDLL("kernel32.dll")
	moveFileExW = kernel32DLL.NewProc("MoveFileExW")
)

const (
	moveFileReplaceExisting = 0x1
	moveFileWriteThrough    = 0x8
)

func replaceArtifactFile(source, destination string) error {
	sourcePtr, err := syscall.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	destinationPtr, err := syscall.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	result, _, callErr := moveFileExW.Call(
		uintptr(unsafe.Pointer(sourcePtr)),
		uintptr(unsafe.Pointer(destinationPtr)),
		moveFileReplaceExisting|moveFileWriteThrough,
	)
	if result == 0 {
		return fmt.Errorf("MoveFileEx: %w", callErr)
	}
	return nil
}
