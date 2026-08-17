package application

import "testing"

func TestWorkspaceLifecycleRoleUsesManagedDirectoriesAndConservativeLegacyInference(t *testing.T) {
	tests := map[string]string{
		"src/main.go":                    WorkspaceLifecycleSource,
		"inputs/spec.pdf":                WorkspaceLifecycleInput,
		"downloads/source.csv":           WorkspaceLifecycleInput,
		"work/demo/generate_charts.py":   WorkspaceLifecycleWorking,
		"img/chart.png":                  WorkspaceLifecycleWorking,
		".extract_pdf.py":                WorkspaceLifecycleTemporary,
		"tmp/render.log":                 WorkspaceLifecycleTemporary,
		"pip/cache/http-v2/body":         WorkspaceLifecycleTemporary,
		"pip":                            WorkspaceLifecycleTemporary,
		"pdf_extract.txt":                WorkspaceLifecycleWorking,
		"pdf_text.json":                  WorkspaceLifecycleWorking,
		"src/__pycache__/parser.pyc":     WorkspaceLifecycleTemporary,
		"web/node_modules/pkg/index.js":  WorkspaceLifecycleTemporary,
		"tools/.venv/Scripts/python.exe": WorkspaceLifecycleTemporary,
		"outputs/demo/index.html":        WorkspaceLifecycleCandidateOutput,
		"系统方案总结报告.md":                    WorkspaceLifecycleCandidateOutput,
		"执行结果.md":                        WorkspaceLifecycleCandidateOutput,
		"final.pdf":                      WorkspaceLifecycleCandidateOutput,
		"index.html":                     WorkspaceLifecycleSource,
	}
	for path, expected := range tests {
		if actual := workspaceLifecycleRole(path, "file"); actual != expected {
			t.Errorf("%s: got %s want %s", path, actual, expected)
		}
	}
}
