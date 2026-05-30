package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCaptureArtifactSnapshot_AddedAndModified(t *testing.T) {
	dir := t.TempDir()
	// Create initial file.
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello"), 0644)

	before, warns := CaptureArtifactSnapshot(dir)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	// Add new file.
	os.WriteFile(filepath.Join(dir, "world.txt"), []byte("world!"), 0644)
	// Modify existing file with different size (triggers modified detection).
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hello, world!"), 0644)
	// Explicitly set modtime to ensure difference.
	future := time.Now().Add(1 * time.Second)
	os.Chtimes(filepath.Join(dir, "hello.txt"), future, future)

	after, warns := CaptureArtifactSnapshot(dir)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	summary := BuildArtifactSummary(before, after, nil)

	if summary.TotalChangedFiles != 2 {
		t.Errorf("expected 2 changed files, got %d", summary.TotalChangedFiles)
	}
	if summary.DiffStat.Added != 1 {
		t.Errorf("expected 1 added, got %d", summary.DiffStat.Added)
	}
	if summary.DiffStat.Modified != 1 {
		t.Errorf("expected 1 modified, got %d", summary.DiffStat.Modified)
	}
	if summary.Summary != "Changed 2 files" {
		t.Errorf("unexpected summary: %q", summary.Summary)
	}
}

func TestCaptureArtifactSnapshot_IgnoresGeneratedPaths(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "src"), 0755)
	os.WriteFile(filepath.Join(dir, "src", "app.ts"), []byte("code"), 0644)

	// Take baseline BEFORE adding ignored files.
	before, _ := CaptureArtifactSnapshot(dir)

	// Add ignored files AFTER baseline — these must NOT appear in changes.
	os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0755)
	os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "new.js"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(dir, ".git", "objects"), 0755)
	os.WriteFile(filepath.Join(dir, ".git", "objects", "new"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(dir, ".env.local"), []byte("SECRET=1"), 0644)
	// Add a real file that SHOULD appear.
	os.WriteFile(filepath.Join(dir, "src", "main.ts"), []byte("new"), 0644)

	after, _ := CaptureArtifactSnapshot(dir)
	summary := BuildArtifactSummary(before, after, nil)

	// Only src/main.ts should appear.
	if summary.TotalChangedFiles != 1 {
		t.Errorf("expected 1 changed file, got %d: %v", summary.TotalChangedFiles, summary.ChangedFiles)
	}
	if len(summary.ChangedFiles) == 1 && summary.ChangedFiles[0].Path != "src/main.ts" {
		t.Errorf("expected src/main.ts, got %s", summary.ChangedFiles[0].Path)
	}
}

func TestBuildArtifactSummary_TruncatesAt20(t *testing.T) {
	before := make(map[string]artifactFileSnapshot)
	after := make(map[string]artifactFileSnapshot)
	for i := 0; i < 25; i++ {
		name := filepath.Join("dir", fmt.Sprintf("file%d.txt", i))
		after[name] = artifactFileSnapshot{Path: name, SizeBytes: int64(i), ModTime: time.Now()}
	}

	summary := BuildArtifactSummary(before, after, nil)

	if summary.TotalChangedFiles != 25 {
		t.Errorf("expected total 25, got %d", summary.TotalChangedFiles)
	}
	if len(summary.ChangedFiles) != 20 {
		t.Errorf("expected 20 in list, got %d", len(summary.ChangedFiles))
	}
	if !summary.Truncated {
		t.Error("expected truncated=true")
	}
}

func TestBuildArtifactSummary_NoChanges(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0644)

	before, _ := CaptureArtifactSnapshot(dir)
	after, _ := CaptureArtifactSnapshot(dir)

	summary := BuildArtifactSummary(before, after, nil)

	if summary.TotalChangedFiles != 0 {
		t.Errorf("expected 0 changes, got %d", summary.TotalChangedFiles)
	}
	if summary.Summary != "No file changes detected" {
		t.Errorf("unexpected summary: %q", summary.Summary)
	}
	if len(summary.ChangedFiles) != 0 {
		t.Errorf("expected empty changed files, got %d", len(summary.ChangedFiles))
	}
}

func TestCaptureArtifactSnapshot_EmptyWorkDir(t *testing.T) {
	snap, warns := CaptureArtifactSnapshot("")
	if len(snap) != 0 {
		t.Errorf("expected empty snapshot, got %d entries", len(snap))
	}
	if len(warns) != 1 || warns[0] != "empty work_dir" {
		t.Errorf("expected 'empty work_dir' warning, got %v", warns)
	}
}

func TestCaptureArtifactSnapshot_NonexistentDir(t *testing.T) {
	snap, warns := CaptureArtifactSnapshot("/nonexistent/path/xyz")
	if len(snap) != 0 {
		t.Errorf("expected empty snapshot, got %d entries", len(snap))
	}
	if len(warns) != 1 {
		t.Errorf("expected 1 warning, got %d", len(warns))
	}
}
