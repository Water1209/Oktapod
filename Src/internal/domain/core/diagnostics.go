package core

type RuntimeDiagnostics struct {
	QueuedRuns       int64
	RunningRuns      int64
	WaitingApprovals int64
	ActiveLeases     int64
}
