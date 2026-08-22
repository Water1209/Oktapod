package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const presetManagedVersion = "1.0.0"

type presetResourceDefinition struct {
	Key          string
	Kind         string
	Name         string
	Description  string
	ManifestJSON string
	Dependencies []string
}

func presetResourceDefinitions() []presetResourceDefinition {
	documentTools := []string{
		core.ToolDocumentOpen, core.ToolDocumentRead, core.ToolDocumentCreate,
		core.ToolDocumentApply, core.ToolDocumentRender, core.ToolDocumentExport,
	}
	definitions := []presetResourceDefinition{
		{
			Key: "document-runtime", Kind: core.CapabilityKindPlugin, Name: "document-runtime",
			Description: "受信、离线、共享的办公文档运行时，为固定 document_* 工具提供解析、修订、渲染和导出能力。",
			ManifestJSON: mustPresetManifest(map[string]any{
				"description": "Oktapod signed document runtime binding",
				"capabilities": []string{"document.runtime"},
				"tools": documentTools,
				"runtime_protocol": "oktapod.document/v1",
			}),
		},
	}
	for _, item := range []struct {
		key, name, description string
		formats                []string
		triggers               []string
	}{
		{"presentations", "presentations", "读取、创建、受控编辑、渲染和导出 PPTX 演示文稿。", []string{"pptx"}, []string{"PPT", "PPTX", "演示文稿", "幻灯片", "slides"}},
		{"pdf", "pdf", "读取、OCR、创建和结构化编辑 PDF，并在导出前完成页面渲染校验。", []string{"pdf"}, []string{"PDF", "扫描件", "OCR", "表单", "合并 PDF"}},
		{"documents", "documents", "读取、创建、受控编辑、渲染和导出 DOCX 文档。", []string{"docx"}, []string{"DOCX", "Word", "文档", "报告"}},
		{"spreadsheets", "spreadsheets", "读取、创建、受控编辑、渲染和导出 XLSX 工作簿。", []string{"xlsx"}, []string{"XLSX", "Excel", "电子表格", "工作簿"}},
	} {
		definitions = append(definitions, presetResourceDefinition{
			Key: item.key, Kind: core.CapabilityKindSkill, Name: item.name, Description: item.description,
			Dependencies: []string{"document.runtime"},
			ManifestJSON: mustPresetManifest(map[string]any{
				"description": item.description,
				"instructions": "优先使用固定 document_* 工具处理冻结的 Asset、Artifact 或 Workspace 文件。先 document_open，再分页 document_read；编辑使用修订号保护的 document_apply；最后一次编辑后必须 document_render，交付必须 document_export。不得生成临时解析脚本、安装依赖、执行宏、刷新外部数据或在 Runtime 不可用时静默降级。",
				"model_invocable": true,
				"triggers": item.triggers,
				"keywords": item.triggers,
				"formats": item.formats,
				"allowed_tools": documentTools,
				"required_capabilities": []string{"document.runtime"},
				"fallback_policy": "strict",
			}),
		})
	}
	return definitions
}

func mustPresetManifest(value map[string]any) string {
	body, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(body)
}

func presetContentHash(definition presetResourceDefinition) string {
	canonical, _ := json.Marshal(struct {
		Key          string   `json:"key"`
		Kind         string   `json:"kind"`
		Name         string   `json:"name"`
		Version      string   `json:"version"`
		ManifestJSON string   `json:"manifest_json"`
		Dependencies []string `json:"dependencies"`
	}{definition.Key, definition.Kind, definition.Name, presetManagedVersion, definition.ManifestJSON, definition.Dependencies})
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (s *Service) ensurePresetResources(ctx context.Context, projectID string) error {
	for _, definition := range presetResourceDefinitions() {
		input := presetCapabilityInput(definition)
		if _, _, err := s.repository.EnsurePresetCapability(ctx, s.ScopeFor(ctx), projectID, input, presetContentHash(definition), false); err != nil {
			return err
		}
	}
	return nil
}

func presetCapabilityInput(definition presetResourceDefinition) core.CreateCapabilityInput {
	return core.CreateCapabilityInput{
		Kind: definition.Kind, Name: definition.Name, Version: presetManagedVersion,
		ManifestJSON: definition.ManifestJSON, ScopeType: core.CapabilityScopeGlobal,
		SourceFilename: "Oktapod Preset Catalog", Origin: core.CapabilityOriginPreset,
		PresetKey: definition.Key, Dependencies: append([]string(nil), definition.Dependencies...),
		ManagedVersion: presetManagedVersion, UpdateState: core.CapabilityUpdateCurrent,
	}
}

func (s *Service) ListResourceCatalog(ctx context.Context, projectID string) ([]core.PresetResourceCatalogItem, error) {
	if err := s.ensurePresetResources(ctx, projectID); err != nil {
		return nil, err
	}
	capabilities, err := s.ListCapabilities(ctx, projectID)
	if err != nil {
		return nil, err
	}
	installed := make(map[string]core.Capability)
	for _, capability := range capabilities {
		if capability.PresetKey != "" {
			installed[capability.PresetKey] = capability
		}
	}
	items := make([]core.PresetResourceCatalogItem, 0, len(presetResourceDefinitions()))
	for _, definition := range presetResourceDefinitions() {
		item := core.PresetResourceCatalogItem{
			PresetKey: definition.Key, Kind: definition.Kind, Name: definition.Name,
			Version: presetManagedVersion, Description: definition.Description,
			Origin: core.CapabilityOriginPreset, Dependencies: append([]string(nil), definition.Dependencies...),
			ManagedVersion: presetManagedVersion, UpdateState: core.CapabilityUpdateCurrent,
			InstallState: "available", Availability: core.ResourceAvailabilityDisabled,
		}
		if capability, ok := installed[definition.Key]; ok {
			item.InstallState, item.CapabilityID, item.Availability = "installed", capability.ID, capability.Availability
			item.UpdateState = capability.UpdateState
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *Service) InstallPresetResource(ctx context.Context, projectID, presetKey string) (core.Capability, error) {
	presetKey = strings.TrimSpace(presetKey)
	for _, definition := range presetResourceDefinitions() {
		if definition.Key != presetKey {
			continue
		}
		// Dependencies are installed first. This is an explicit user resource
		// management action, never a model-triggered repair.
		for _, dependency := range definition.Dependencies {
			if dependency == "document.runtime" {
				for _, candidate := range presetResourceDefinitions() {
					if candidate.Key == "document-runtime" {
						if _, _, err := s.repository.EnsurePresetCapability(ctx, s.ScopeFor(ctx), projectID, presetCapabilityInput(candidate), presetContentHash(candidate), true); err != nil {
							return core.Capability{}, err
						}
					}
				}
			}
		}
		capability, installed, err := s.repository.EnsurePresetCapability(ctx, s.ScopeFor(ctx), projectID, presetCapabilityInput(definition), presetContentHash(definition), true)
		if err != nil {
			return core.Capability{}, err
		}
		if !installed {
			return core.Capability{}, fmt.Errorf("preset resource was not installed")
		}
		capability = s.decorateCapabilityAvailability(ctx, capability)
		return capability, nil
	}
	return core.Capability{}, &core.ValidationError{Field: "preset_key", Message: "is not in the signed preset catalog"}
}

func decorateDependencyAvailability(items []core.Capability) {
	provided := make(map[string]bool)
	for _, item := range items {
		if item.Status != core.CapabilityStatusEnabled || item.Availability != core.ResourceAvailabilityAvailable {
			continue
		}
		var manifest map[string]any
		if json.Unmarshal([]byte(item.ManifestJSON), &manifest) == nil {
			for _, capability := range manifestStringValues(manifest["capabilities"]) {
				provided[capability] = true
			}
		}
	}
	for index := range items {
		for _, dependency := range items[index].Dependencies {
			if !provided[dependency] && items[index].Status == core.CapabilityStatusEnabled {
				items[index].Availability = core.ResourceAvailabilityDegraded
				items[index].UpdateState = core.CapabilityUpdateDegraded
			}
		}
	}
}

func manifestStringValues(value any) []string {
	raw, _ := value.([]any)
	items := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
			items = append(items, strings.TrimSpace(text))
		}
	}
	sort.Strings(items)
	return items
}

func skillManifestDependencies(manifestJSON string) []string {
	var manifest map[string]any
	if json.Unmarshal([]byte(manifestJSON), &manifest) != nil {
		return nil
	}
	dependencies := manifestStringValues(manifest["required_capabilities"])
	if len(dependencies) == 0 {
		dependencies = manifestStringValues(manifest["required-capabilities"])
	}
	return dependencies
}

func documentPresetForTask(assets []map[string]any, intent string) string {
	intent = strings.ToLower(intent)
	intentLooksDocument := strings.TrimSpace(intent) == ""
	for _, keyword := range []string{"read", "summar", "extract", "edit", "modify", "create", "generate", "render", "export", "读取", "总结", "提取", "编辑", "修改", "创建", "生成", "渲染", "导出", "分析"} {
		if strings.Contains(intent, keyword) {
			intentLooksDocument = true
			break
		}
	}
	if !intentLooksDocument {
		return ""
	}
	for _, asset := range assets {
		name, _ := asset["name"].(string)
		mediaType, _ := asset["media_type"].(string)
		format := strings.ToLower(strings.TrimPrefix(filepath.Ext(name), "."))
		if strings.Contains(mediaType, "presentationml") || format == "pptx" {
			return "presentations"
		}
		if mediaType == "application/pdf" || format == "pdf" {
			return "pdf"
		}
		if strings.Contains(mediaType, "wordprocessingml") || format == "docx" {
			return "documents"
		}
		if strings.Contains(mediaType, "spreadsheetml") || format == "xlsx" {
			return "spreadsheets"
		}
	}
	return ""
}
