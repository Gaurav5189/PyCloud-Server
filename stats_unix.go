//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func collectPlatformStats(rootDir string) *SystemStats {
	stats := &SystemStats{
		DiskPercent: "N/A",
		DiskFreeGB:  "N/A",
		DiskTotalGB: "N/A",
		CPULoad:     "N/A",
		RAMUsage:    "N/A",
	}

	// 1. Disk usage via statfs
	var stat syscall.Statfs_t
	if err := syscall.Statfs(rootDir, &stat); err == nil {
		totalBytes := stat.Blocks * uint64(stat.Bsize)
		freeBytes := stat.Bavail * uint64(stat.Bsize)
		usedBytes := totalBytes - freeBytes

		if totalBytes > 0 {
			pct := (float64(usedBytes) / float64(totalBytes)) * 100.0
			stats.DiskPercent = fmt.Sprintf("%.1f%%", pct)
			stats.DiskFreeGB = fmt.Sprintf("%.1f GB", float64(freeBytes)/(1024*1024*1024))
			stats.DiskTotalGB = fmt.Sprintf("%.1f GB", float64(totalBytes)/(1024*1024*1024))
		}
	}

	// 2. CPU load via /proc/loadavg or syscall
	if data, err := os.ReadFile("/proc/loadavg"); err == nil {
		fields := strings.Fields(string(data))
		if len(fields) >= 1 {
			stats.CPULoad = fields[0]
		}
	}

	// 3. RAM usage via /proc/meminfo
	if f, err := os.Open("/proc/meminfo"); err == nil {
		defer f.Close()
		scanner := bufio.NewScanner(f)
		var memTotal, memAvailable int64
		for scanner.Scan() {
			line := scanner.Text()
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				key := strings.TrimSuffix(parts[0], ":")
				val, _ := strconv.ParseInt(parts[1], 10, 64)
				switch key {
				case "MemTotal":
					memTotal = val
				case "MemAvailable":
					memAvailable = val
				}
			}
		}
		if memTotal > 0 && memAvailable > 0 {
			used := memTotal - memAvailable
			pct := (float64(used) / float64(memTotal)) * 100.0
			stats.RAMUsage = fmt.Sprintf("%.1f%%", pct)
		}
	}

	return stats
}
