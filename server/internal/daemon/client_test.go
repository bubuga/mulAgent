package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
)

func TestClient_IdentityHeaders_PostJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Client-Platform"); got != "daemon" {
			t.Errorf("expected X-Client-Platform daemon, got %q", got)
		}
		if got := r.Header.Get("X-Client-Version"); got != "9.9.9" {
			t.Errorf("expected X-Client-Version 9.9.9, got %q", got)
		}
		if got := r.Header.Get("X-Client-OS"); got != normalizeGOOS(runtime.GOOS) {
			t.Errorf("expected X-Client-OS %q, got %q", normalizeGOOS(runtime.GOOS), got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("expected Authorization Bearer tok, got %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"ok": "1"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.SetToken("tok")
	c.SetVersion("9.9.9")

	if err := c.postJSON(context.Background(), "/api/daemon/test", map[string]any{}, nil); err != nil {
		t.Fatalf("postJSON: %v", err)
	}
}

func TestClient_IdentityHeaders_GetJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Client-Platform"); got != "daemon" {
			t.Errorf("expected X-Client-Platform daemon, got %q", got)
		}
		if got := r.Header.Get("X-Client-Version"); got != "1.2.3" {
			t.Errorf("expected X-Client-Version 1.2.3, got %q", got)
		}
		if got := r.Header.Get("X-Client-OS"); got == "" {
			t.Errorf("expected X-Client-OS to be set")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.SetToken("tok")
	c.SetVersion("1.2.3")

	var out map[string]any
	if err := c.getJSON(context.Background(), "/api/daemon/test", &out); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
}

func TestClient_VersionOmittedWhenUnset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Client-Platform"); got != "daemon" {
			t.Errorf("expected X-Client-Platform daemon, got %q", got)
		}
		// SetVersion not called → header must be omitted (not "").
		if vals := r.Header.Values("X-Client-Version"); len(vals) != 0 {
			t.Errorf("expected X-Client-Version absent, got %v", vals)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	if err := c.postJSON(context.Background(), "/api/daemon/test", nil, nil); err != nil {
		t.Fatalf("postJSON: %v", err)
	}
}

func TestNormalizeGOOS(t *testing.T) {
	cases := map[string]string{
		"darwin":  "macos",
		"windows": "windows",
		"linux":   "linux",
		"freebsd": "freebsd",
	}
	for in, want := range cases {
		if got := normalizeGOOS(in); got != want {
			t.Errorf("normalizeGOOS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestClient_PinTaskSessionSendsBaseRevision(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.SetToken("tok")

	baseRev := &RevisionInfo{Kind: "git", Head: "abc123", DirtyCount: 2}
	err := c.PinTaskSession(context.Background(), "task-1", "", "/work/dir", baseRev, []string{"warn1"})
	if err != nil {
		t.Fatalf("PinTaskSession: %v", err)
	}

	if captured["base_revision"] == nil {
		t.Fatal("expected base_revision in request body")
	}
	rev, ok := captured["base_revision"].(map[string]any)
	if !ok {
		t.Fatalf("expected base_revision to be a map, got %T", captured["base_revision"])
	}
	if rev["kind"] != "git" {
		t.Errorf("expected kind=git, got %v", rev["kind"])
	}
	if rev["head"] != "abc123" {
		t.Errorf("expected head=abc123, got %v", rev["head"])
	}
	warnings, ok := captured["revision_warnings"].([]any)
	if !ok || len(warnings) != 1 || warnings[0] != "warn1" {
		t.Errorf("expected revision_warnings=[warn1], got %v", captured["revision_warnings"])
	}
}

func TestClient_CompleteTaskSendsResultRevision(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "completed"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.SetToken("tok")

	resultRev := &RevisionInfo{Kind: "git", Head: "def456", DirtyCount: 5}
	err := c.CompleteTask(context.Background(), "task-1", "done", "", "sess-1", "/work", nil, resultRev, nil)
	if err != nil {
		t.Fatalf("CompleteTask: %v", err)
	}

	if captured["output"] != "done" {
		t.Errorf("expected output=done, got %v", captured["output"])
	}
	if captured["result_revision"] == nil {
		t.Fatal("expected result_revision in request body")
	}
	rev := captured["result_revision"].(map[string]any)
	if rev["head"] != "def456" {
		t.Errorf("expected head=def456, got %v", rev["head"])
	}
	// base_revision should be absent (nil passed)
	if _, ok := captured["base_revision"]; ok {
		t.Error("expected base_revision to be absent when nil")
	}
}

func TestClient_FailTaskSendsRevisionWarnings(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "failed"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.SetToken("tok")

	warnings := []string{"git status timeout", "dirty hash incomplete"}
	err := c.FailTask(context.Background(), "task-1", "crash", "", "", "agent_error", nil, nil, warnings)
	if err != nil {
		t.Fatalf("FailTask: %v", err)
	}

	if captured["error"] != "crash" {
		t.Errorf("expected error=crash, got %v", captured["error"])
	}
	got, ok := captured["revision_warnings"].([]any)
	if !ok {
		t.Fatalf("expected revision_warnings array, got %T", captured["revision_warnings"])
	}
	if len(got) != 2 || got[0] != "git status timeout" || got[1] != "dirty hash incomplete" {
		t.Errorf("expected revision_warnings=[...], got %v", got)
	}
}

func TestClient_PinTaskSessionSkipsWhenAllEmpty(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer srv.Close()

	c := NewClient(srv.URL)
	c.SetToken("tok")

	err := c.PinTaskSession(context.Background(), "task-1", "", "", nil, nil)
	if err != nil {
		t.Fatalf("PinTaskSession: %v", err)
	}
	if called {
		t.Error("expected no HTTP call when all params are empty")
	}
}
