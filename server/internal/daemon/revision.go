package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os/exec"
	"sort"
	"strings"
	"time"
)

const maxDirtyPaths = 50

// CaptureRevision captures the current workspace revision (git HEAD + dirty state).
// Returns a RevisionInfo that can be sent to the server via PinTaskSession/CompleteTask.
// Never returns an error — failures are captured as kind="error" with a warning.
func CaptureRevision(ctx context.Context, workDir string) RevisionInfo {
	if workDir == "" {
		return RevisionInfo{Kind: "none", Warning: "empty work_dir"}
	}

	// D7: Use git rev-parse --is-inside-work-tree to detect git repos.
	out, err := runGit(ctx, workDir, "rev-parse", "--is-inside-work-tree")
	if err != nil || out != "true" {
		return RevisionInfo{Kind: "none", Warning: "not a git repo"}
	}

	// Capture HEAD
	head, err := runGit(ctx, workDir, "rev-parse", "HEAD")
	if err != nil {
		return RevisionInfo{Kind: "error", Warning: "git rev-parse HEAD failed: " + err.Error()}
	}

	// D22: Capture dirty state using porcelain=v1 for stable parsing.
	porcelain, err := runGit(ctx, workDir, "status", "--porcelain=v1")
	if err != nil {
		return RevisionInfo{
			Kind:    "git",
			Head:    head,
			Warning: "git status failed: " + err.Error(),
		}
	}

	lines := splitNonEmpty(porcelain)
	if len(lines) == 0 {
		// Clean repo
		return RevisionInfo{Kind: "git", Head: head}
	}

	// D22: Dirty hash = sha256 of sorted full porcelain lines (consistency guaranteed).
	sorted := make([]string, len(lines))
	copy(sorted, lines)
	sort.Strings(sorted)
	hash := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	dirtyHash := hex.EncodeToString(hash[:])

	// D22: Extract dirty paths best-effort. Handle rename lines (R old -> new → take new).
	dirtyPaths := extractDirtyPaths(lines)

	return RevisionInfo{
		Kind:       "git",
		Head:       head,
		DirtyHash:  dirtyHash,
		DirtyCount: len(lines),
		DirtyPaths: dirtyPaths,
	}
}

// runGit executes a git command with a 5-second timeout.
func runGit(ctx context.Context, workDir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", workDir}, args...)...)
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

func splitNonEmpty(s string) []string {
	var result []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			result = append(result, line)
		}
	}
	return result
}

// extractDirtyPaths extracts file paths from porcelain status lines.
// D22: Handles rename lines (R old -> new → takes new path).
// Paths capped at maxDirtyPaths.
func extractDirtyPaths(porcelain []string) []string {
	var paths []string
	seen := make(map[string]bool)
	for _, line := range porcelain {
		if len(line) < 4 {
			continue
		}
		// Porcelain format: XY <path> or XY orig -> new (rename)
		path := strings.TrimSpace(line[3:])
		// D22: Handle rename: "R  old -> new" → extract "new"
		if strings.Contains(path, " -> ") {
			parts := strings.SplitN(path, " -> ", 2)
			if len(parts) == 2 {
				path = strings.TrimSpace(parts[1])
			}
		}
		if path != "" && !seen[path] {
			seen[path] = true
			paths = append(paths, path)
			if len(paths) >= maxDirtyPaths {
				break
			}
		}
	}
	return paths
}
