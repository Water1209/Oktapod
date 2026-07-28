package application

import (
	"context"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const (
	maxImportConversations = 1000
	maxImportMessages      = 100000
	maxImportMessageBytes  = 1 << 20
	maxImportTotalBytes    = 50 << 20
)

func (s *Service) ImportTranscript(ctx context.Context, projectID string, input core.ImportTranscriptInput, idempotencyKey string) (core.ImportTranscriptResult, error) {
	if _, err := s.principalFor(ctx); err != nil {
		return core.ImportTranscriptResult{}, err
	}
	input.SourceFormat = strings.TrimSpace(input.SourceFormat)
	if input.SourceFormat != "oktapod-browser-export-v1" && input.SourceFormat != "pi-jsonl-preview-v1" {
		return core.ImportTranscriptResult{}, &core.ValidationError{Field: "source_format", Message: "must be a supported portable transcript format"}
	}
	if len(input.Conversations) == 0 || len(input.Conversations) > maxImportConversations {
		return core.ImportTranscriptResult{}, &core.ValidationError{Field: "conversations", Message: "must contain 1 to 1000 conversations"}
	}
	messageCount := 0
	totalBytes := 0
	for conversationIndex := range input.Conversations {
		conversation := &input.Conversations[conversationIndex]
		conversation.Title = strings.TrimSpace(conversation.Title)
		if len([]rune(conversation.Title)) > 200 {
			return core.ImportTranscriptResult{}, &core.ValidationError{Field: "conversations.title", Message: "must not exceed 200 characters"}
		}
		if len(conversation.Messages) == 0 {
			return core.ImportTranscriptResult{}, &core.ValidationError{Field: "conversations.messages", Message: "must not be empty"}
		}
		for messageIndex := range conversation.Messages {
			message := &conversation.Messages[messageIndex]
			switch message.Role {
			case string(core.MessageRoleUser), string(core.MessageRoleAssistant), string(core.MessageRoleTool):
			default:
				return core.ImportTranscriptResult{}, &core.ValidationError{Field: "conversations.messages.role", Message: "must be user, assistant, or tool"}
			}
			if !utf8.ValidString(message.Content) || strings.ContainsRune(message.Content, 0) || len([]byte(message.Content)) > maxImportMessageBytes {
				return core.ImportTranscriptResult{}, &core.ValidationError{Field: "conversations.messages.content", Message: "must be bounded UTF-8 text"}
			}
			if message.CreatedAt.IsZero() {
				message.CreatedAt = time.Now().UTC()
			}
			totalBytes += len([]byte(message.Content))
			messageCount++
		}
	}
	if messageCount > maxImportMessages || totalBytes > maxImportTotalBytes {
		return core.ImportTranscriptResult{}, &core.ValidationError{Field: "conversations", Message: "transcript exceeds the import size limit"}
	}
	idempotency := core.Idempotency{
		Key:         idempotencyKey,
		RequestHash: requestHash(input),
	}
	return retryTransientSQLiteValue(ctx, func() (core.ImportTranscriptResult, error) {
		return s.repository.ImportTranscript(ctx, s.ScopeFor(ctx), projectID, input, idempotency)
	})
}
