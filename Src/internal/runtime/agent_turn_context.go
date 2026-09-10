package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

func (w *Worker) contextForPrompt(ctx context.Context, run core.RunContext) (string, map[string]any) {
	return contextForPrompt(ctx, w.repository, run)
}

func contextForPrompt(ctx context.Context, repository WorkerRepository, run core.RunContext) (string, map[string]any) {
	var config map[string]any
	plan := map[string]any{"requested_assets": 0, "metadata_assets": 0, "loaded_assets": 0, "requested_memories": 0, "loaded_memories": 0, "bytes_loaded": 0, "estimated_tokens": 0, "skipped": 0, "memory_context_source": "none", "legacy_memory_context": false}
	if json.Unmarshal([]byte(run.ConfigSnapshotJSON), &config) != nil {
		return "", plan
	}
	assetIDs := snapshotStringList(config["context_asset_ids"])
	memoryIDs := snapshotStringList(config["context_memory_ids"])
	plan["requested_assets"] = len(assetIDs)
	plan["requested_memories"] = len(memoryIDs)
	if len(assetIDs) == 0 && len(memoryIDs) == 0 {
		return "", plan
	}
	const maxBytes = 24 * 1024
	remaining := maxBytes
	sections := make([]string, 0, len(assetIDs)+len(memoryIDs))
	scope := core.Scope{OrganizationID: run.OrganizationID, UserID: run.OwnerUserID}
	for _, assetID := range assetIDs {
		frozen, frozenOK := frozenAssetFromSnapshot(run.ConfigSnapshotJSON, assetID)
		if frozenOK {
			metadata := fmt.Sprintf("[附件元数据] asset_id=%s | name=%s | media_type=%s | size_bytes=%d | content_hash=%s\n完整内容未内联时，调用 asset_stage(asset_id) 获取只读临时路径后再交给脚本处理。", frozen.ID, frozen.Name, frozen.MediaType, frozen.SizeBytes, frozen.ContentHash)
			if len(metadata) <= remaining {
				sections = append(sections, metadata)
				remaining -= len(metadata)
				plan["metadata_assets"] = plan["metadata_assets"].(int) + 1
				plan["bytes_loaded"] = plan["bytes_loaded"].(int) + len(metadata)
			}
		}
		if remaining <= 0 {
			plan["skipped"] = plan["skipped"].(int) + 1
			continue
		}
		asset, reader, err := repository.OpenAssetContent(ctx, scope, assetID)
		if err != nil || asset.ProjectID != run.ProjectID {
			plan["skipped"] = plan["skipped"].(int) + 1
			continue
		}
		if frozenOK && (asset.ContentHash != frozen.ContentHash || asset.SizeBytes != frozen.SizeBytes || asset.Name != frozen.Name || asset.MediaType != frozen.MediaType) {
			_ = reader.Close()
			plan["skipped"] = plan["skipped"].(int) + 1
			continue
		}
		if !isTextAsset(asset.MediaType, asset.Name) {
			_ = reader.Close()
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(reader, int64(remaining)+1))
		_ = reader.Close()
		if readErr != nil || len(data) == 0 {
			plan["skipped"] = plan["skipped"].(int) + 1
			continue
		}
		if len(data) > remaining {
			data = data[:remaining]
		}
		remaining -= len(data)
		sections = append(sections, "[资产 "+asset.Name+" | "+asset.ContentHash+"]\n"+string(data))
		plan["loaded_assets"] = plan["loaded_assets"].(int) + 1
		plan["bytes_loaded"] = plan["bytes_loaded"].(int) + len(data)
	}
	if len(memoryIDs) > 0 {
		if frozenValue, ok := config["context_memory_snapshots"]; ok {
			plan["memory_context_source"] = "frozen_snapshot"
			encoded, _ := json.Marshal(frozenValue)
			var snapshots []application.ContextMemorySnapshot
			if json.Unmarshal(encoded, &snapshots) != nil {
				plan["skipped"] = plan["skipped"].(int) + len(memoryIDs)
			} else {
				byID := make(map[string]application.ContextMemorySnapshot, len(snapshots))
				for _, snapshot := range snapshots {
					byID[snapshot.ID] = snapshot
				}
				for _, memoryID := range memoryIDs {
					memory, ok := byID[memoryID]
					if !ok || remaining <= 0 || memory.Content == "" {
						plan["skipped"] = plan["skipped"].(int) + 1
						continue
					}
					data := []byte(memory.Content)
					if len(data) > remaining {
						data = data[:remaining]
					}
					remaining -= len(data)
					sections = append(sections, "[记忆 "+memory.Title+" | "+memory.SourceHash+"]\n"+string(data))
					plan["loaded_memories"] = plan["loaded_memories"].(int) + 1
					plan["bytes_loaded"] = plan["bytes_loaded"].(int) + len(data)
				}
			}
		} else if remaining > 0 {
			// Historical Runs predate bounded Memory payload snapshots. Preserve
			// recovery compatibility, but expose the non-deterministic fallback.
			plan["memory_context_source"] = "legacy_live_ids"
			plan["legacy_memory_context"] = true
			memories, err := repository.ListMemories(ctx, scope, run.ProjectID, "", 500)
			if err == nil {
				byID := make(map[string]core.Memory, len(memories))
				for _, memory := range memories {
					byID[memory.ID] = memory
				}
				for _, memoryID := range memoryIDs {
					memory, ok := byID[memoryID]
					if !ok || remaining <= 0 {
						plan["skipped"] = plan["skipped"].(int) + 1
						continue
					}
					data := []byte(memory.Content)
					if len(data) > remaining {
						data = data[:remaining]
					}
					remaining -= len(data)
					sections = append(sections, "[记忆 "+memory.Title+" | "+memory.SourceHash+"]\n"+string(data))
					plan["loaded_memories"] = plan["loaded_memories"].(int) + 1
					plan["bytes_loaded"] = plan["bytes_loaded"].(int) + len(data)
				}
			}
		}
	}
	plan["estimated_tokens"] = plan["bytes_loaded"].(int) / 4
	return strings.Join(sections, "\n\n"), plan
}

func snapshotStringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		if value, ok := item.(string); ok && strings.TrimSpace(value) != "" {
			result = append(result, value)
		}
	}
	return result
}

func isTextAsset(mediaType, name string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") ||
		mediaType == "application/xml" || strings.HasSuffix(mediaType, "+xml") ||
		mediaType == "application/javascript" || mediaType == "application/x-javascript" {
		return true
	}
	ext := strings.ToLower(name)
	for _, suffix := range []string{".md", ".txt", ".go", ".ts", ".tsx", ".js", ".json", ".yaml", ".yml", ".sql", ".py", ".java", ".rs", ".sh"} {
		if strings.HasSuffix(ext, suffix) {
			return true
		}
	}
	return false
}
