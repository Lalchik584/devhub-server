package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── Models ──

type HealthResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Version string `json:"version"`
}

type Snapshot struct {
	Name      string `json:"name"`
	Project   string `json:"project"`
	Timestamp string `json:"timestamp"`
	Size      int64  `json:"size"`
}

type SystemStats struct {
	CPU        float64         `json:"cpu"`
	RAM        float64         `json:"ram"`
	Disk       float64         `json:"disk"`
	Uptime     string          `json:"uptime"`
	Containers []ContainerInfo `json:"containers"`
	GoVersion  string          `json:"go_version"`
	NumCPU     int             `json:"num_cpu"`
	TotalRAM   uint64          `json:"total_ram"`
	TotalDisk  uint64          `json:"total_disk"`
}

type ContainerInfo struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// ── Config ──

var (
	snapshotsDir = "/opt/devhub/snapshots"
	projectsDir  = "/opt/devhub/projects"
	fcmScript    = "/tmp/send_fcm.py"
)

// ── Middleware ──

func corsMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next(w, r)
	}
}

// ── Health ──

func healthHandler(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(HealthResponse{
		Status:  "ok",
		Service: "DevHub Engine",
		Version: "1.0.0",
	})
}

// ── Snapshots ──

func createSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "project required", http.StatusBadRequest)
		return
	}
	projectDir := filepath.Join(projectsDir, project)
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		http.Error(w, "project not found", http.StatusNotFound)
		return
	}
	os.MkdirAll(snapshotsDir, 0755)
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	snapshotName := fmt.Sprintf("%s_%s", project, timestamp)
	snapshotPath := filepath.Join(snapshotsDir, snapshotName)
	copyDir(projectDir, snapshotPath)
	size, _ := dirSize(snapshotPath)

	sendPush("📸 Снапшот", fmt.Sprintf("%s — создан", snapshotName))

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(Snapshot{
		Name: snapshotName, Project: project, Timestamp: timestamp, Size: size,
	})
}

func listSnapshotsHandler(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	os.MkdirAll(snapshotsDir, 0755)
	entries, _ := os.ReadDir(snapshotsDir)
	var snapshots []Snapshot
	for _, e := range entries {
		if !e.IsDir() { continue }
		name := e.Name()
		if project != "" && !strings.HasPrefix(name, project+"_") { continue }
		size, _ := dirSize(filepath.Join(snapshotsDir, name))
		parts := strings.SplitN(name, "_", 2)
		proj, ts := parts[0], ""
		if len(parts) > 1 { ts = parts[1] }
		snapshots = append(snapshots, Snapshot{Name: name, Project: proj, Timestamp: ts, Size: size})
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Timestamp > snapshots[j].Timestamp })
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(snapshots)
}

func restoreSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	snapshotPath := filepath.Join(snapshotsDir, name)
	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	parts := strings.SplitN(name, "_", 2)
	project := parts[0]
	os.RemoveAll(filepath.Join(projectsDir, project))
	copyDir(snapshotPath, filepath.Join(projectsDir, project))

	sendPush("🔄 Восстановление", fmt.Sprintf("%s — восстановлен", name))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func deleteSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name required", http.StatusBadRequest)
		return
	}
	os.RemoveAll(filepath.Join(snapshotsDir, name))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// ── System Stats ──

func systemStatsHandler(w http.ResponseWriter, r *http.Request) {
	stats := SystemStats{GoVersion: runtime.Version(), NumCPU: runtime.NumCPU()}

	if out, err := exec.Command("sh", "-c", "top -bn1 | grep 'Cpu(s)' | awk '{print $2+$4}'").Output(); err == nil {
		if val, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err == nil {
			stats.CPU = float64(int(val*10)) / 10
		}
	}
	if out, err := exec.Command("sh", "-c", "free -b | grep Mem").Output(); err == nil {
		f := strings.Fields(string(out))
		if len(f) >= 3 {
			total, _ := strconv.ParseUint(f[1], 10, 64)
			used, _ := strconv.ParseUint(f[2], 10, 64)
			stats.TotalRAM = total
			if total > 0 { stats.RAM = float64(int(float64(used)/float64(total)*1000)) / 10 }
		}
	}
	if out, err := exec.Command("sh", "-c", "df -B1 / | tail -1").Output(); err == nil {
		f := strings.Fields(string(out))
		if len(f) >= 4 {
			total, _ := strconv.ParseUint(f[1], 10, 64)
			used, _ := strconv.ParseUint(f[2], 10, 64)
			stats.TotalDisk = total
			if total > 0 { stats.Disk = float64(int(float64(used)/float64(total)*1000)) / 10 }
		}
	}
	if out, err := exec.Command("uptime", "-p").Output(); err == nil {
		stats.Uptime = strings.TrimSpace(strings.TrimPrefix(string(out), "up "))
	}
	if out, err := exec.Command("docker", "ps", "--format", "{{.Names}}|{{.Status}}").Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" { continue }
			parts := strings.SplitN(line, "|", 2)
			if len(parts) == 2 {
				stats.Containers = append(stats.Containers, ContainerInfo{Name: parts[0], Status: parts[1]})
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// ── Deploy Webhook ──

func deployHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var payload map[string]interface{}
	json.NewDecoder(r.Body).Decode(&payload)
	repo, _ := payload["repository"].(map[string]interface{})
	repoName := ""
	if repo != nil {
		repoName = repo["name"].(string)
	}
	log.Printf("Deploy webhook: %s", repoName)
	sendPush("🚀 Деплой", fmt.Sprintf("%s — запущен", repoName))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "project": repoName})
}

// ── Helpers ──

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil { return err }
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if info.IsDir() { return os.MkdirAll(target, info.Mode()) }
		srcFile, _ := os.Open(path); defer srcFile.Close()
		dstFile, _ := os.Create(target); defer dstFile.Close()
		io.Copy(dstFile, srcFile)
		return nil
	})
}

func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil { return err }
		if !info.IsDir() { size += info.Size() }
		return nil
	})
	return size, err
}

func sendPush(title, body string) {
	go func() {
		cmd := exec.Command("python3", fcmScript, title, body)
		output, err := cmd.CombinedOutput()
		if err != nil {
			log.Printf("FCM error: %v, output: %s", err, output)
		} else {
			log.Printf("FCM sent: %s - %s", title, body)
		}
	}()
}

// ── Main ──

func main() {
	os.MkdirAll(snapshotsDir, 0755)

	http.HandleFunc("/api/health", corsMiddleware(healthHandler))
	http.HandleFunc("/api/snapshots/create", corsMiddleware(createSnapshotHandler))
	http.HandleFunc("/api/snapshots/list", corsMiddleware(listSnapshotsHandler))
	http.HandleFunc("/api/snapshots/restore", corsMiddleware(restoreSnapshotHandler))
	http.HandleFunc("/api/snapshots/delete", corsMiddleware(deleteSnapshotHandler))
	http.HandleFunc("/api/system/stats", corsMiddleware(systemStatsHandler))
	http.HandleFunc("/api/webhook/deploy", corsMiddleware(deployHandler))

	log.Println("DevHub Engine v1.0 запущен на порту 5050")
	log.Fatal(http.ListenAndServe(":5050", nil))
}
