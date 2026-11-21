package main

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

type SystemStats struct {
	DiskPercent string `json:"disk_pct"`
	DiskFreeGB  string `json:"free_gb"`
	DiskTotalGB string `json:"total_gb"`
	CPULoad     string `json:"cpu_load"`
	RAMUsage    string `json:"ram_usage"`
	Timestamp   int64  `json:"timestamp"`
}

type StatsCollector struct {
	mu          sync.RWMutex
	cachedStats *SystemStats
	lastUpdate  time.Time
	cacheTTL    time.Duration
	rootDir     string
}

func NewStatsCollector(rootDir string) *StatsCollector {
	return &StatsCollector{
		cacheTTL: 2 * time.Second,
		rootDir:  rootDir,
	}
}

func (sc *StatsCollector) GetStats() *SystemStats {
	sc.mu.RLock()
	if sc.cachedStats != nil && time.Since(sc.lastUpdate) < sc.cacheTTL {
		defer sc.mu.RUnlock()
		return sc.cachedStats
	}
	sc.mu.RUnlock()

	sc.mu.Lock()
	defer sc.mu.Unlock()

	// Double check after lock
	if sc.cachedStats != nil && time.Since(sc.lastUpdate) < sc.cacheTTL {
		return sc.cachedStats
	}

	stats := collectPlatformStats(sc.rootDir)
	stats.Timestamp = time.Now().Unix()

	sc.cachedStats = stats
	sc.lastUpdate = time.Now()
	return stats
}

func HandleStats(collector *StatsCollector) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats := collector.GetStats()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stats)
	}
}
