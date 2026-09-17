package main

import (
	"encoding/json"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

type SystemStats struct {
	CPU        float64            `json:"cpu"`
	RAM        float64            `json:"ram"`
	Disk       float64            `json:"disk"`
	Uptime     string             `json:"uptime"`
	Containers []ContainerInfo    `json:"containers"`
	GoVersion  string             `json:"go_version"`
	NumCPU     int                `json:"num_cpu"`
	TotalRAM   uint64             `json:"total_ram"`
	TotalDisk  uint64             `json:"total_disk"`
}

type ContainerInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Uptime string `json:"uptime"`
}

func systemStatsHandler(w http.ResponseWriter, r *http.Request) {
	stats := SystemStats{
		GoVersion: runtime.Version(),
		NumCPU:    runtime.NumCPU(),
	}

	// CPU
	if percent, err := cpu.Percent(time.Second, false); err == nil && len(percent) > 0 {
		stats.CPU = round(percent[0], 1)
	}

	// RAM
	if v, err := mem.VirtualMemory(); err == nil {
		stats.RAM = round(v.UsedPercent, 1)
		stats.TotalRAM = v.Total
	}

	// Disk
	if d, err := disk.Usage("/"); err == nil {
		stats.Disk = round(d.UsedPercent, 1)
		stats.TotalDisk = d.Total
	}

	// Uptime
	if out, err := exec.Command("uptime", "-p").Output(); err == nil {
		stats.Uptime = strings.TrimSpace(strings.TrimPrefix(string(out), "up "))
	}

	// Docker containers
	if out, err := exec.Command("docker", "ps", "--format", "{{.Names}}|{{.Status}}").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for _, line := range lines {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, "|", 2)
			if len(parts) == 2 {
				stats.Containers = append(stats.Containers, ContainerInfo{
					Name:   parts[0],
					Status: parts[1],
				})
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func round(val float64, precision int) float64 {
	format := strconv.FormatFloat(val, 'f', precision, 64)
	result, _ := strconv.ParseFloat(format, 64)
	return result
}
