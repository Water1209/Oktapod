package application

import (
	"context"
	"fmt"
	"strings"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

var branchSummaryHeadings = []string{
	"## Branch Intent",
	"## Completed",
	"## Key Decisions",
	"## Open Items",
	"## Handoff Context",
}

func (s *Service) ListSessionEntries(ctx context.Context, conversationID string, afterEntrySeq int64, limit int) (core.SessionTreePage, error) {
	return s.repository.ListSessionEntries(ctx, s.ScopeFor(ctx), conversationID, afterEntrySeq, limit)
}

func (s *Service) GetSessionEntryPath(ctx context.Context, entryID string) ([]core.SessionEntry, error) {
	return s.repository.GetSessionEntryPath(ctx, s.ScopeFor(ctx), entryID)
}

func (s *Service) NavigateSession(ctx context.Context, conversationID string, input core.NavigateSessionInput) (core.NavigateSessionResult, error) {
	return s.repository.NavigateSession(ctx, s.ScopeFor(ctx), conversationID, input)
}

func (s *Service) ForkSession(ctx context.Context, conversationID string, input core.ForkSessionInput) (core.Conversation, error) {
	return s.repository.ForkSession(ctx, s.ScopeFor(ctx), conversationID, input)
}

func (s *Service) CloneSession(ctx context.Context, conversationID, title string) (core.Conversation, error) {
	conversation, err := s.repository.GetConversation(ctx, s.ScopeFor(ctx), conversationID)
	if err != nil {
		return core.Conversation{}, err
	}
	return s.repository.ForkSession(ctx, s.ScopeFor(ctx), conversationID, core.ForkSessionInput{
		EntryID: conversation.ActiveLeafEntryID,
		Title:   title,
		Clone:   true,
	})
}

func (s *Service) LabelSessionEntry(ctx context.Context, conversationID, entryID string, input core.LabelSessionEntryInput) (core.SessionEntry, error) {
	return s.repository.LabelSessionEntry(ctx, s.ScopeFor(ctx), conversationID, entryID, input)
}

func (s *Service) AppendBranchSummary(ctx context.Context, conversationID string, input core.BranchSummaryInput) (core.SessionEntry, error) {
	if strings.TrimSpace(input.Summary) == "" {
		conversation, err := s.repository.GetConversation(ctx, s.ScopeFor(ctx), conversationID)
		if err != nil {
			return core.SessionEntry{}, err
		}
		if conversation.ActiveLeafEntryID != input.ExpectedLeafEntryID {
			return core.SessionEntry{}, &core.StateConflictError{Status: "active_leaf_changed", Command: "branch_summary"}
		}
		messages := make([]core.Message, 0, conversationMessagePageSize)
		after := int64(0)
		for {
			page, pageErr := s.repository.ListSessionPathMessages(ctx, s.ScopeFor(ctx), conversationID, input.ExpectedLeafEntryID, after, conversationMessagePageSize)
			if pageErr != nil {
				return core.SessionEntry{}, pageErr
			}
			messages = append(messages, page...)
			if len(page) < conversationMessagePageSize {
				break
			}
			after = page[len(page)-1].ServerSeq
		}
		result, err := s.completeProjectTurn(ctx, branchSummarySystemPrompt, branchSummaryPrompt+"\n\n<branch>\n"+renderConversationContext("", messages)+"</branch>", 2_048, "", "", "")
		if err != nil {
			return core.SessionEntry{}, err
		}
		input.Summary = strings.TrimSpace(result.Content)
		if !validBranchSummary(input.Summary) {
			return core.SessionEntry{}, &ModelTurnUnavailableError{Err: fmt.Errorf("branch summary did not return the required five-section handoff")}
		}
		entry, err := s.repository.AppendBranchSummary(ctx, s.ScopeFor(ctx), conversationID, input)
		if err != nil {
			return core.SessionEntry{}, err
		}
		if err := s.repository.RecordConversationModelCalls(ctx, s.ScopeFor(ctx), conversation.ProjectID, conversationID, []core.ConversationModelCall{{Purpose: "branch_summary", ThroughEntryID: entry.ID, Model: result}}); err != nil {
			return core.SessionEntry{}, err
		}
		return entry, nil
	}
	if !validBranchSummary(input.Summary) {
		return core.SessionEntry{}, &core.ValidationError{Field: "summary", Message: "must use the exact five-section branch summary format"}
	}
	return s.repository.AppendBranchSummary(ctx, s.ScopeFor(ctx), conversationID, input)
}

func validBranchSummary(summary string) bool {
	position := -1
	for _, heading := range branchSummaryHeadings {
		next := strings.Index(summary[position+1:], heading)
		if next < 0 {
			return false
		}
		position += next + 1
	}
	return true
}

const branchSummaryPrompt = `Summarize the selected session branch for a future continuation.
Do not use the six-section conversation compaction format.
Use this EXACT five-section format:

## Branch Intent
[What this branch attempted]

## Completed
- [Completed work and verified outcomes]

## Key Decisions
- **[Decision]**: [Rationale]

## Open Items
- [Unfinished work or "(none)"]

## Handoff Context
- [Exact paths, identifiers, errors, and facts needed to continue]`

const branchSummarySystemPrompt = `You summarize one selected session branch for a future continuation.
Do not continue the conversation and do not answer its questions.
Only output the exact five-section branch handoff requested by the user prompt.`
