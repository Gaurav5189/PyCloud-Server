//go:build windows

package main

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	getDiskFreeSpaceExW = kernel32.NewProc("GetDiskFreeSpaceExW")
)

func collectPlatformStats(rootDir string) *SystemStats {
	stats := &SystemStats{
		DiskPercent: "N/A",
		DiskFreeGB:  "N/A",
		DiskTotalGB: "N/A",
		CPULoad:     "N/A",
		RAMUsage:    "N/A",
	}

	// Windows Disk Space API
	rootDirUTF16, err := syscall.UTF16PtrFromString(rootDir)
	if err == nil {
		var freeBytesAvailable, totalNumberOfBytes, totalNumberOfFreeBytes int64
		r1, _, _ := getDiskFreeSpaceExW.Call(
			uintptr(unsafe.Pointer(rootDirUTF16)),
			uintptr(unsafe.Pointer(&freeBytesAvailable)),
			uintptr(unsafe.Pointer(&totalNumberOfBytes)),
			uintptr(unsafe.Pointer(&totalNumberOfFreeBytes)),
		)
		if r1 != 0 && totalNumberOfBytes > 0 {
			usedBytes := totalNumberOfBytes - totalNumberOfFreeBytes
			pct := (float64(usedBytes) / float64(totalNumberOfBytes)) * 100.0
			stats.DiskPercent = fmt.Sprintf("%.1f%%", pct)
			stats.DiskFreeGB = fmt.Sprintf("%.1f GB", float64(totalNumberOfFreeBytes)/(1024*1024*1024))
			stats.DiskTotalGB = fmt.Sprintf("%.1f GB", float64(totalNumberOfBytes)/(1024*1024*1024))
		}
	}

	return stats
}
