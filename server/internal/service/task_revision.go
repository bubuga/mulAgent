package service

// TaskRevisionInfo carries git revision data for step-linked tasks.
// Handler and service share this type. Daemon has its own mirror (RevisionInfo).
// DB storage: TEXT columns via json.Marshal → pgtype.Text.
type TaskRevisionInfo struct {
	Kind       string   `json:"kind"`
	Head       string   `json:"head,omitempty"`
	DirtyHash  string   `json:"dirty_hash,omitempty"`
	DirtyCount int      `json:"dirty_count,omitempty"`
	DirtyPaths []string `json:"dirty_paths,omitempty"`
	Warning    string   `json:"warning,omitempty"`
}

// TaskRevisionUpdate carries revision data passed to CompleteTask/FailTask.
// Defined in service package to avoid handler → service import cycle.
type TaskRevisionUpdate struct {
	BaseRevision     *TaskRevisionInfo
	ResultRevision   *TaskRevisionInfo
	RevisionWarnings []string
}
