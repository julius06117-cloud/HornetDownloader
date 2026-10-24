package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDetailsRejectsBadInput(t *testing.T) {
	a := &App{cfg: Config{DownloadDir: t.TempDir()}, res: newResilience()}
	rr := httptest.NewRecorder()
	a.handleDetails(rr, httptest.NewRequest(http.MethodPost, "/api/details", bytes.NewReader([]byte(`{}`))))
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out["error"] == nil {
		t.Fatalf("want error for empty gid: %v", out)
	}
}

func TestDetailsUnknownTab(t *testing.T) {
	a := &App{cfg: Config{DownloadDir: t.TempDir()}, res: newResilience()}
	body, _ := json.Marshal(map[string]any{"gid": "aria-1", "tab": "nope"})
	rr := httptest.NewRecorder()
	a.handleDetails(rr, httptest.NewRequest(http.MethodPost, "/api/details", bytes.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out["error"] == nil {
		t.Fatalf("want unknown tab error: %v", out)
	}
}

func TestReselectRequiresGID(t *testing.T) {
	a := &App{cfg: Config{DownloadDir: t.TempDir()}, res: newResilience()}
	rr := httptest.NewRecorder()
	a.handleReselect(rr, httptest.NewRequest(http.MethodPost, "/api/reselect", bytes.NewReader([]byte(`{"skip":true}`))))
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out["error"] == nil {
		t.Fatalf("want error: %v", out)
	}
}

func TestRecommendAPI(t *testing.T) {
	a := &App{cfg: Config{DownloadDir: t.TempDir(), DefaultEngine: "aria2"}, res: newResilience()}
	body, _ := json.Marshal(map[string]any{"url": testMagnet, "engine": "auto"})
	rr := httptest.NewRecorder()
	a.handleRecommend(rr, httptest.NewRequest(http.MethodPost, "/api/recommend", bytes.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if out["recommended"] != EngineBT {
		t.Fatalf("magnet recommend bt, got %v", out)
	}
	if out["resolved"] != EngineBT {
		t.Fatalf("auto resolves to bt despite pref aria2, got %v", out)
	}
}
