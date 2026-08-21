package application

const MaxContextMemorySnapshotBytes = 24 * 1024

// ContextMemorySnapshot is the bounded Memory payload authorized for one Run.
// It is immutable Run configuration, not a second Memory record.
type ContextMemorySnapshot struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	SourceHash string `json:"source_hash"`
}
