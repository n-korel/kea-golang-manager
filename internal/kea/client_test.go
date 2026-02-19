package kea

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClient_HAHeartbeat_Success(t *testing.T) {
	response := map[string]interface{}{
		"result": 0,
		"text":   "HA status successful",
		"arguments": map[string]interface{}{
			"state": "hot-standby",
		},
	}
	body, _ := json.Marshal(response)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client := NewClient(server.URL, 5*time.Second)
	ctx := context.Background()

	state, err := client.HAHeartbeat(ctx)
	if err != nil {
		t.Fatalf("HAHeartbeat: unexpected error: %v", err)
	}
	if state != "hot-standby" {
		t.Errorf("HAHeartbeat: got state %q, want %q", state, "hot-standby")
	}
}

func TestClient_HAHeartbeat_ResultNotZero(t *testing.T) {
	response := map[string]interface{}{
		"result": 1,
		"text":   "HA not configured",
		"arguments": map[string]interface{}{
			"state": "waiting",
		},
	}
	body, _ := json.Marshal(response)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer server.Close()

	client := NewClient(server.URL, 5*time.Second)
	ctx := context.Background()

	state, err := client.HAHeartbeat(ctx)
	if err == nil {
		t.Fatalf("HAHeartbeat: expected error for result=1, got state=%q", state)
	}
	if state != "" {
		t.Errorf("HAHeartbeat: expected empty state on error, got %q", state)
	}
}

func TestClient_HAHeartbeat_NetworkError(t *testing.T) {
	// Подключение к неслушающему порту — гарантированная сетевая ошибка (connection refused)
	client := NewClient("http://127.0.0.1:39999", 100*time.Millisecond)
	ctx := context.Background()

	_, err := client.HAHeartbeat(ctx)
	if err == nil {
		t.Fatal("HAHeartbeat: expected error on unreachable node")
	}
}
