package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

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

type EventMessage struct {
	Type      string `json:"type"`
	Project   string `json:"project"`
	Author    string `json:"author"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

var (
	snapshotsDir = "/home/admin_ilka/devhub-snapshots"
	mqttClient   mqtt.Client
)

func initMQTT() {
	opts := mqtt.NewClientOptions()
	opts.AddBroker("tcp://localhost:1883")
	opts.SetClientID("devhub-engine")
	opts.SetAutoReconnect(true)

	mqttClient = mqtt.NewClient(opts)
	if token := mqttClient.Connect(); token.Wait() && token.Error() != nil {
		log.Printf("MQTT warning: %v (продолжаем без MQTT)", token.Error())
	} else {
		log.Println("MQTT подключён")
	}
}

func publishEvent(event EventMessage) {
	if mqttClient == nil || !mqttClient.IsConnected() {
		return
	}
	data, _ := json.Marshal(event)
	topic := fmt.Sprintf("devhub/events/%s", event.Project)
	mqttClient.Publish(topic, 0, false, data)
	mqttClient.Publish("devhub/events/all", 0, false, data)
	log.Printf("MQTT published: %s -> %s", topic, event.Type)
}

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
		Version: "0.4.0",
	})
}

// ── Webhook ──
func webhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Failed to read body", http.StatusBadRequest)
		return
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	repo, _ := payload["repository"].(map[string]interface{})
	repoName := ""
	if repo != nil {
		repoName, _ = repo["name"].(string)
	}

	pusher, _ := payload["pusher"].(map[string]interface{})
	author := ""
	if pusher != nil {
		author, _ = pusher["login"].(string)
	}

	commits, _ := payload["commits"].([]interface{})
	commitMsg := ""
	if len(commits) > 0 {
		if c, ok := commits[0].(map[string]interface{}); ok {
			commitMsg, _ = c["message"].(string)
		}
	}

	event := EventMessage{
		Type:      "push",
		Project:   repoName,
		Author:    author,
		Message:   commitMsg,
		Timestamp: time.Now().Format(time.RFC3339),
	}

	publishEvent(event)

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "event": "published"})
	log.Printf("Webhook: push to %s by %s", repoName, author)
}

// ── Snapshots ──
func createSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	project := r.URL.Query().Get("project")
	if project == "" {
		http.Error(w, "project parameter required", http.StatusBadRequest)
		return
	}

	projectDir := filepath.Join("/home/admin_ilka/devhub/projects", project)
	if _, err := os.Stat(projectDir); os.IsNotExist(err) {
		http.Error(w, "project directory not found", http.StatusNotFound)
		return
	}

	timestamp := time.Now().Format("2006-01-02_15-04-05")
	snapshotName := fmt.Sprintf("%s_%s", project, timestamp)
	snapshotPath := filepath.Join(snapshotsDir, snapshotName)

	os.MkdirAll(snapshotsDir, 0755)

	if err := copyDir(projectDir, snapshotPath); err != nil {
		log.Printf("Error creating snapshot: %v", err)
		http.Error(w, "failed to create snapshot", http.StatusInternalServerError)
		return
	}

	size, _ := dirSize(snapshotPath)

	snapshot := Snapshot{
		Name:      snapshotName,
		Project:   project,
		Timestamp: timestamp,
		Size:      size,
	}

	publishEvent(EventMessage{
		Type:      "snapshot_created",
		Project:   project,
		Message:   fmt.Sprintf("Snapshot %s created", snapshotName),
		Timestamp: time.Now().Format(time.RFC3339),
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(snapshot)
	log.Printf("Snapshot created: %s", snapshotName)
}

func listSnapshotsHandler(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	os.MkdirAll(snapshotsDir, 0755)

	entries, err := os.ReadDir(snapshotsDir)
	if err != nil {
		http.Error(w, "failed to read snapshots", http.StatusInternalServerError)
		return
	}

	var snapshots []Snapshot
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if project != "" && !strings.HasPrefix(name, project+"_") {
			continue
		}

		size, _ := dirSize(filepath.Join(snapshotsDir, name))
		parts := strings.SplitN(name, "_", 2)
		proj := parts[0]
		ts := ""
		if len(parts) > 1 {
			ts = parts[1]
		}

		snapshots = append(snapshots, Snapshot{
			Name:      name,
			Project:   proj,
			Timestamp: ts,
			Size:      size,
		})
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].Timestamp > snapshots[j].Timestamp
	})

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
		http.Error(w, "name parameter required", http.StatusBadRequest)
		return
	}

	snapshotPath := filepath.Join(snapshotsDir, name)
	if _, err := os.Stat(snapshotPath); os.IsNotExist(err) {
		http.Error(w, "snapshot not found", http.StatusNotFound)
		return
	}

	parts := strings.SplitN(name, "_", 2)
	project := parts[0]
	projectDir := filepath.Join("/home/admin_ilka/devhub/projects", project)

	os.RemoveAll(projectDir)

	if err := copyDir(snapshotPath, projectDir); err != nil {
		log.Printf("Error restoring snapshot: %v", err)
		http.Error(w, "failed to restore snapshot", http.StatusInternalServerError)
		return
	}

	publishEvent(EventMessage{
		Type:      "snapshot_restored",
		Project:   project,
		Message:   fmt.Sprintf("Snapshot %s restored", name),
		Timestamp: time.Now().Format(time.RFC3339),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": fmt.Sprintf("Snapshot %s restored", name)})
	log.Printf("Snapshot restored: %s", name)
}

func deleteSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "name parameter required", http.StatusBadRequest)
		return
	}

	snapshotPath := filepath.Join(snapshotsDir, name)
	if err := os.RemoveAll(snapshotPath); err != nil {
		http.Error(w, "failed to delete snapshot", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "message": fmt.Sprintf("Snapshot %s deleted", name)})
	log.Printf("Snapshot deleted: %s", name)
}

// ── Events (SSE) ──
func eventsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming not supported", http.StatusInternalServerError)
		return
	}

	messageChan := make(chan []byte, 10)

	opts := mqtt.NewClientOptions()
	opts.AddBroker("tcp://localhost:1883")
	opts.SetClientID("devhub-sse-" + fmt.Sprintf("%d", time.Now().UnixNano()))
	client := mqtt.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		http.Error(w, "MQTT unavailable", http.StatusServiceUnavailable)
		return
	}
	defer client.Disconnect(250)

	client.Subscribe("devhub/events/all", 0, func(c mqtt.Client, m mqtt.Message) {
		select {
		case messageChan <- m.Payload():
		default:
		}
	})

	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	for {
		select {
		case msg := <-messageChan:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// ── Helpers ──
func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relPath, _ := filepath.Rel(src, path)
		targetPath := filepath.Join(dst, relPath)
		if info.IsDir() {
			return os.MkdirAll(targetPath, info.Mode())
		}
		srcFile, err := os.Open(path)
		if err != nil {
			return err
		}
		defer srcFile.Close()
		dstFile, err := os.Create(targetPath)
		if err != nil {
			return err
		}
		defer dstFile.Close()
		_, err = io.Copy(dstFile, srcFile)
		return err
	})
}

func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// ── Main ──
func main() {
	os.MkdirAll(snapshotsDir, 0755)
	initMQTT()

	http.HandleFunc("/api/health", corsMiddleware(healthHandler))
	http.HandleFunc("/api/webhook/gitea", corsMiddleware(webhookHandler))
	http.HandleFunc("/api/snapshots/create", corsMiddleware(createSnapshotHandler))
	http.HandleFunc("/api/snapshots/list", corsMiddleware(listSnapshotsHandler))
	http.HandleFunc("/api/snapshots/restore", corsMiddleware(restoreSnapshotHandler))
	http.HandleFunc("/api/snapshots/delete", corsMiddleware(deleteSnapshotHandler))
	http.HandleFunc("/api/events", corsMiddleware(eventsHandler))
	http.HandleFunc("/api/system/stats", corsMiddleware(systemStatsHandler))

	log.Println("DevHub Engine v0.4.0 запущен на порту 5050")
	log.Fatal(http.ListenAndServe(":5050", nil))
}
