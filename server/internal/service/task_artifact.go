package service

// TaskArtifactSummary mirrors daemon.ArtifactSummary for the handler/service layer.
type TaskArtifactSummary struct {
	Version           int                       `json:"version"`
	Summary           string                    `json:"summary"`
	ChangedFiles      []TaskArtifactChangedFile `json:"changed_files"`
	TotalChangedFiles int                       `json:"total_changed_files"`
	Truncated         bool                      `json:"truncated"`
	DiffStat          TaskArtifactDiffStat      `json:"diff_stat"`
	Warnings          []string                  `json:"warnings,omitempty"`
}

type TaskArtifactChangedFile struct {
	Path       string `json:"path"`
	ChangeType string `json:"change_type"`
	SizeBytes  int64  `json:"size_bytes"`
}

type TaskArtifactDiffStat struct {
	Added    int `json:"added"`
	Modified int `json:"modified"`
}
