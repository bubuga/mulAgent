package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// artifactFileSnapshot holds metadata for a single file in the workspace.
type artifactFileSnapshot struct {
	Path      string
	SizeBytes int64
	ModTime   time.Time
}

// artifactIgnoredDirs are directory names skipped during workspace walk.
var artifactIgnoredDirs = map[string]bool{
	".git":           true,
	".agent_context": true,
	"node_modules":   true,
	".next":          true,
	".turbo":         true,
	"dist":           true,
	"build":          true,
	"coverage":       true,
}

const maxArtifactChangedFiles = 20

// CaptureArtifactSnapshot walks workDir and returns a map of relative path →
// file metadata, plus any non-fatal warnings. Failures are captured as
// warnings, never returned as errors.
func CaptureArtifactSnapshot(workDir string) (map[string]artifactFileSnapshot, []string) {
	if workDir == "" {
		return map[string]artifactFileSnapshot{}, []string{"empty work_dir"}
	}
	if _, err := os.Stat(workDir); err != nil {
		return map[string]artifactFileSnapshot{}, []string{fmt.Sprintf("stat work_dir: %v", err)}
	}

	snap := make(map[string]artifactFileSnapshot)
	var warnings []string

	err := filepath.Walk(workDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible entries
		}
		name := info.Name()

		// Skip ignored directories (don't descend).
		if info.IsDir() {
			if artifactIgnoredDirs[name] {
				return filepath.SkipDir
			}
			// Skip .env* directories (e.g. .env.d/).
			if strings.HasPrefix(name, ".env") {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip symlinks.
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}

		// Skip .env* files anywhere.
		if strings.HasPrefix(name, ".env") {
			return nil
		}

		rel, err := filepath.Rel(workDir, path)
		if err != nil {
			return nil
		}
		// Normalize to forward slashes for cross-platform consistency.
		rel = filepath.ToSlash(rel)

		snap[rel] = artifactFileSnapshot{
			Path:      rel,
			SizeBytes: info.Size(),
			ModTime:   info.ModTime(),
		}
		return nil
	})
	if err != nil {
		warnings = append(warnings, fmt.Sprintf("snapshot walk error: %v", err))
	}

	return snap, warnings
}

// BuildArtifactSummary compares before/after snapshots and returns a summary
// of added and modified files.
func BuildArtifactSummary(before, after map[string]artifactFileSnapshot, warnings []string) ArtifactSummary {
	var changed []ArtifactChangedFile
	added := 0
	modified := 0

	for path, afterFile := range after {
		beforeFile, existed := before[path]
		if !existed {
			changed = append(changed, ArtifactChangedFile{
				Path:       path,
				ChangeType: "added",
				SizeBytes:  afterFile.SizeBytes,
			})
			added++
		} else if afterFile.SizeBytes != beforeFile.SizeBytes || !afterFile.ModTime.Equal(beforeFile.ModTime) {
			changed = append(changed, ArtifactChangedFile{
				Path:       path,
				ChangeType: "modified",
				SizeBytes:  afterFile.SizeBytes,
			})
			modified++
		}
	}

	// Sort by path for deterministic output.
	sort.Slice(changed, func(i, j int) bool {
		return changed[i].Path < changed[j].Path
	})

	total := len(changed)
	truncated := false
	if total > maxArtifactChangedFiles {
		changed = changed[:maxArtifactChangedFiles]
		truncated = true
	}

	summary := "No file changes detected"
	if total == 1 {
		summary = "Changed 1 file"
	} else if total > 1 {
		summary = fmt.Sprintf("Changed %d files", total)
	}

	return ArtifactSummary{
		Version:           1,
		Summary:           summary,
		ChangedFiles:      changed,
		TotalChangedFiles: total,
		Truncated:         truncated,
		DiffStat:          ArtifactDiffStat{Added: added, Modified: modified},
		Warnings:          warnings,
	}
}
