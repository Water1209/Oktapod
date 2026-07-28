package application

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

const (
	DefaultConversationTriggerTokens    int64 = 100_000
	DefaultConversationRetainTokens     int64 = 20_000
	DefaultConversationSummaryMaxTokens int64 = 4_096
	conversationMessagePageSize               = 500
)

type ConversationCompactionOutcome struct {
	Compaction   *core.ConversationCompaction
	BeforeTokens int64
	AfterTokens  int64
	Retained     int
}

type conversationContextUsageReader interface {
	LatestConversationContextUsage(ctx context.Context, scope core.Scope, conversationID string) (core.ConversationContextUsage, error)
}

type conversationFileContextReader interface {
	ConversationFileContext(ctx context.Context, scope core.Scope, conversationID string, through time.Time) (core.ConversationFileContext, error)
}

func (s *Service) GetConversationContextSettings(ctx context.Context) (core.ConversationContextSettings, error) {
	return s.repository.GetConversationContextSettings(ctx, s.ScopeFor(ctx))
}

func (s *Service) UpdateConversationContextSettings(ctx context.Context, input core.UpdateConversationContextSettingsInput) (core.ConversationContextSettings, error) {
	return s.repository.UpsertConversationContextSettings(ctx, s.ScopeFor(ctx), input)
}

func (s *Service) CompactConversation(ctx context.Context, conversationID, customInstructions, model, fallback string) (ConversationCompactionOutcome, error) {
	conversation, err := s.repository.GetConversation(ctx, s.ScopeFor(ctx), strings.TrimSpace(conversationID))
	if err != nil {
		return ConversationCompactionOutcome{}, err
	}
	return s.compactConversationContext(ctx, conversation, "", customInstructions, model, fallback, true)
}

func (s *Service) conversationContextForPrompt(ctx context.Context, conversationID, pendingContent, model, fallback string) (string, error) {
	messages, err := s.conversationTranscriptForPrompt(ctx, conversationID, pendingContent, model, fallback)
	if err != nil {
		return "", err
	}
	return RenderLegacyTranscript(messages), nil
}

func (s *Service) conversationTranscriptForPrompt(ctx context.Context, conversationID, pendingContent, model, fallback string) ([]core.ModelMessage, error) {
	if strings.TrimSpace(conversationID) == "" {
		return TranscriptAssembler{}.Conversation("", nil, pendingContent), nil
	}
	conversation, err := s.repository.GetConversation(ctx, s.ScopeFor(ctx), conversationID)
	if err != nil {
		return nil, err
	}
	if _, err := s.compactConversationContext(ctx, conversation, pendingContent, "", model, fallback, false); err != nil {
		return nil, err
	}
	checkpoint, messages, err := s.loadConversationContext(ctx, conversation.ID)
	if err != nil {
		return nil, err
	}
	return TranscriptAssembler{}.Conversation(checkpoint.Summary, messages, pendingContent), nil
}

func (s *Service) compactConversationContext(ctx context.Context, conversation core.Conversation, pendingContent, customInstructions, model, fallback string, force bool) (ConversationCompactionOutcome, error) {
	settings, err := s.repository.GetConversationContextSettings(ctx, s.ScopeFor(ctx))
	if errors.Is(err, sql.ErrNoRows) {
		scope := s.ScopeFor(ctx)
		settings = core.ConversationContextSettings{
			OrganizationID:   scope.OrganizationID,
			OwnerUserID:      scope.UserID,
			AutoCompact:      true,
			TriggerTokens:    DefaultConversationTriggerTokens,
			RetainTokens:     DefaultConversationRetainTokens,
			SummaryMaxTokens: DefaultConversationSummaryMaxTokens,
		}
		err = nil
	}
	if err != nil {
		return ConversationCompactionOutcome{}, err
	}
	checkpoint, messages, err := s.loadConversationContext(ctx, conversation.ID)
	if err != nil {
		return ConversationCompactionOutcome{}, err
	}
	beforeTokens := s.estimateConversationContextTokens(ctx, checkpoint, messages, pendingContent)
	outcome := ConversationCompactionOutcome{BeforeTokens: beforeTokens, AfterTokens: beforeTokens, Retained: len(messages)}
	if !force && (!settings.AutoCompact || beforeTokens <= settings.TriggerTokens) {
		return outcome, nil
	}

	selection := selectConversationCompaction(checkpoint, messages, settings.RetainTokens)
	if len(selection.Source) == 0 || len(selection.Retained) == 0 {
		return outcome, nil
	}
	source := selection.Source
	retained := selection.Retained
	fileContext := s.conversationFileContext(ctx, conversation.ID, source[len(source)-1].CreatedAt)
	sourceTokens := estimateConversationTextTokens(checkpoint.Summary) + estimateConversationMessagesTokens(source)
	if sourceTokens <= 0 {
		return outcome, nil
	}

	var summaryResult core.ModelResult
	for attempt := 0; attempt < 3; attempt++ {
		summaryResult, err = s.completeProjectTurn(
			ctx,
			conversationCompactionSystemPrompt,
			conversationCompactionPrompt(checkpoint.Summary, source, fileContext, customInstructions),
			int(settings.SummaryMaxTokens),
			model,
			fallback,
			"",
		)
		if err == nil && validConversationCompactionSummary(summaryResult.Content) {
			break
		}
		if ctx.Err() != nil {
			return ConversationCompactionOutcome{}, ctx.Err()
		}
	}
	if err != nil {
		return ConversationCompactionOutcome{}, err
	}
	summary := strings.TrimSpace(summaryResult.Content)
	if !validConversationCompactionSummary(summary) {
		return ConversationCompactionOutcome{}, &ModelTurnUnavailableError{Err: fmt.Errorf("conversation compaction did not return the required six-section checkpoint")}
	}
	trigger := "auto"
	if force {
		trigger = "manual"
	}
	item, err := s.repository.SaveConversationCompaction(ctx, s.ScopeFor(ctx), core.ConversationCompaction{
		ProjectID:           conversation.ProjectID,
		ConversationID:      conversation.ID,
		Summary:             summary,
		CompactedThroughSeq: selection.CompactedThroughSeq,
		RetainedFromSeq:     selection.RetainedFromSeq,
		SourceMessageCount:  int64(len(source)),
		SourceTokens:        sourceTokens,
		Trigger:             trigger,
		SplitMessageID:      selection.SplitMessageID,
		SplitPrefixRunes:    selection.SplitPrefixRunes,
		ExpectedLeafEntryID: conversation.ActiveLeafEntryID,
	})
	if err != nil {
		return ConversationCompactionOutcome{}, err
	}
	if err := s.repository.RecordConversationModelCalls(ctx, s.ScopeFor(ctx), conversation.ProjectID, conversation.ID, []core.ConversationModelCall{{Purpose: "context_compaction:" + trigger, ThroughEntryID: item.EntryID, Model: summaryResult}}); err != nil {
		return ConversationCompactionOutcome{}, err
	}
	outcome.Compaction = &item
	outcome.Retained = len(retained)
	outcome.AfterTokens = estimateConversationTextTokens(summary) + estimateConversationMessagesTokens(retained) + estimateConversationTextTokens(pendingContent)
	return outcome, nil
}

func (s *Service) conversationFileContext(ctx context.Context, conversationID string, through time.Time) core.ConversationFileContext {
	reader, ok := s.repository.(conversationFileContextReader)
	if !ok {
		return core.ConversationFileContext{}
	}
	files, err := reader.ConversationFileContext(ctx, s.ScopeFor(ctx), conversationID, through)
	if err != nil {
		return core.ConversationFileContext{}
	}
	return files
}

func (s *Service) estimateConversationContextTokens(ctx context.Context, checkpoint core.ConversationCompaction, messages []core.Message, pendingContent string) int64 {
	fallback := estimateConversationTextTokens(checkpoint.Summary) + estimateConversationMessagesTokens(messages) + estimateConversationTextTokens(pendingContent)
	reader, ok := s.repository.(conversationContextUsageReader)
	if !ok {
		return fallback
	}
	usage, err := reader.LatestConversationContextUsage(ctx, s.ScopeFor(ctx), firstConversationID(messages, checkpoint))
	if err != nil || usage.CreatedAt.IsZero() || (!checkpoint.CreatedAt.IsZero() && !usage.CreatedAt.After(checkpoint.CreatedAt)) {
		return fallback
	}
	baseline := usage.ContextTokens
	if baseline <= 0 {
		baseline = usage.InputTokens
		if usage.SemanticsVersion == "v2" {
			baseline += usage.CacheReadTokens
		}
	}
	if baseline <= 0 {
		return fallback
	}
	// ContextTokens is the provider-observed input window. The assistant output
	// becomes history immediately after that call, then only messages created
	// after the call and the pending composer content need deterministic tail
	// estimation.
	baseline += usage.OutputTokens
	for _, message := range messages {
		if message.CreatedAt.After(usage.CreatedAt) {
			baseline += estimateConversationMessageTokens(message)
		}
	}
	return baseline + estimateConversationTextTokens(pendingContent)
}

func firstConversationID(messages []core.Message, checkpoint core.ConversationCompaction) string {
	if checkpoint.ConversationID != "" {
		return checkpoint.ConversationID
	}
	for _, message := range messages {
		if message.ConversationID != "" {
			return message.ConversationID
		}
	}
	return ""
}

func (s *Service) loadConversationContext(ctx context.Context, conversationID string) (core.ConversationCompaction, []core.Message, error) {
	conversation, err := s.repository.GetConversation(ctx, s.ScopeFor(ctx), conversationID)
	if err != nil {
		return core.ConversationCompaction{}, nil, err
	}
	checkpoint, err := s.repository.GetLatestConversationCompaction(ctx, s.ScopeFor(ctx), conversationID)
	if err != nil && !errorsIsNoRows(err) {
		return core.ConversationCompaction{}, nil, err
	}
	after := checkpoint.CompactedThroughSeq
	messages := make([]core.Message, 0, conversationMessagePageSize)
	for {
		var page []core.Message
		var pageErr error
		if conversation.ActiveLeafEntryID == "" {
			page, pageErr = s.repository.ListMessages(ctx, s.ScopeFor(ctx), conversationID, after, conversationMessagePageSize)
		} else {
			page, pageErr = s.repository.ListSessionPathMessages(ctx, s.ScopeFor(ctx), conversationID, conversation.ActiveLeafEntryID, after, conversationMessagePageSize)
		}
		if pageErr != nil {
			return core.ConversationCompaction{}, nil, pageErr
		}
		messages = append(messages, page...)
		if len(page) < conversationMessagePageSize {
			break
		}
		next := page[len(page)-1].ServerSeq
		if next <= after {
			return core.ConversationCompaction{}, nil, fmt.Errorf("conversation message cursor did not advance")
		}
		after = next
	}
	if checkpoint.SplitMessageID != "" && checkpoint.SplitPrefixRunes > 0 {
		for index := range messages {
			if messages[index].ID != checkpoint.SplitMessageID {
				continue
			}
			content := []rune(messages[index].Content)
			if checkpoint.SplitPrefixRunes >= int64(len(content)) {
				messages = append(messages[:index], messages[index+1:]...)
			} else {
				messages[index].Content = string(content[checkpoint.SplitPrefixRunes:])
			}
			break
		}
	}
	return checkpoint, messages, nil
}

func errorsIsNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }

func conversationCompactionCut(messages []core.Message, retainTokens int64) int {
	if len(messages) < 2 {
		return 0
	}
	retainedTokens := int64(0)
	cut := len(messages)
	for cut > 0 && retainedTokens < retainTokens {
		cut--
		retainedTokens += estimateConversationMessageTokens(messages[cut])
	}
	// Keep a whole conversational turn: a retained assistant/tool message must
	// keep the user message that initiated it.
	for cut > 0 && messages[cut].Role != "user" {
		cut--
	}
	return cut
}

type conversationCompactionSelection struct {
	Source              []core.Message
	Retained            []core.Message
	CompactedThroughSeq int64
	RetainedFromSeq     int64
	SplitMessageID      string
	SplitPrefixRunes    int64
}

func selectConversationCompaction(checkpoint core.ConversationCompaction, messages []core.Message, retainTokens int64) conversationCompactionSelection {
	cut := conversationCompactionCut(messages, retainTokens)
	if cut > 0 && cut < len(messages) {
		return conversationCompactionSelection{
			Source:              append([]core.Message(nil), messages[:cut]...),
			Retained:            append([]core.Message(nil), messages[cut:]...),
			CompactedThroughSeq: messages[cut-1].ServerSeq,
			RetainedFromSeq:     messages[cut].ServerSeq,
		}
	}
	if len(messages) < 2 {
		return conversationCompactionSelection{}
	}
	first := messages[0]
	later := append([]core.Message(nil), messages[1:]...)
	available := retainTokens - estimateConversationMessagesTokens(later)
	// A split checkpoint keeps a suffix of the oversized message while the
	// summary receives its prefix. Do not split the same persisted message
	// twice; the next checkpoint advances past its remaining suffix.
	if first.ServerSeq > 0 && checkpoint.SplitMessageID != first.ID && available > 0 {
		prefix, suffix, prefixRunes := splitConversationMessagePrefix(first.Content, available)
		if strings.TrimSpace(prefix) != "" && strings.TrimSpace(suffix) != "" {
			sourceMessage := first
			sourceMessage.Content = prefix
			retainedMessage := first
			retainedMessage.Content = suffix
			return conversationCompactionSelection{
				Source:              []core.Message{sourceMessage},
				Retained:            append([]core.Message{retainedMessage}, later...),
				CompactedThroughSeq: first.ServerSeq - 1,
				RetainedFromSeq:     first.ServerSeq,
				SplitMessageID:      first.ID,
				SplitPrefixRunes:    int64(prefixRunes),
			}
		}
	}
	// When a whole later message already consumes the retain budget, summarize
	// the first message as the turn prefix and retain the rest of the turn.
	return conversationCompactionSelection{
		Source:              []core.Message{first},
		Retained:            later,
		CompactedThroughSeq: first.ServerSeq,
		RetainedFromSeq:     later[0].ServerSeq,
	}
}

func splitConversationMessagePrefix(content string, retainTokens int64) (prefix, suffix string, prefixRunes int) {
	runes := []rune(content)
	if len(runes) < 2 || retainTokens <= 0 {
		return content, "", len(runes)
	}
	retainedQuarterTokens := int64(0)
	targetQuarterTokens := retainTokens * 4
	index := len(runes)
	for index > 0 && retainedQuarterTokens < targetQuarterTokens {
		index--
		if runes[index] <= unicode.MaxASCII {
			retainedQuarterTokens++
		} else {
			retainedQuarterTokens += 4
		}
	}
	if index <= 0 || index >= len(runes) {
		return content, "", len(runes)
	}
	return string(runes[:index]), string(runes[index:]), index
}

func estimateConversationMessagesTokens(messages []core.Message) int64 {
	var tokens int64
	for _, message := range messages {
		tokens += estimateConversationMessageTokens(message)
	}
	return tokens
}

func estimateConversationMessageTokens(message core.Message) int64 {
	return 4 + estimateConversationTextTokens(message.Role) + estimateConversationTextTokens(message.Content)
}

// ASCII prose/code averages close to four characters per token. CJK and
// other non-ASCII text is counted conservatively as one token per rune so the
// auto-compactor triggers before, rather than after, the provider limit.
func estimateConversationTextTokens(value string) int64 {
	var ascii, other int64
	for _, r := range value {
		if r <= unicode.MaxASCII {
			ascii++
		} else {
			other++
		}
	}
	return (ascii+3)/4 + other
}

func renderConversationContext(summary string, messages []core.Message) string {
	var value strings.Builder
	if strings.TrimSpace(summary) != "" {
		value.WriteString("[Conversation summary]\n")
		value.WriteString(strings.TrimSpace(summary))
		value.WriteString("\n\n[Recent conversation]\n")
	}
	for _, message := range messages {
		value.WriteString(message.Role)
		value.WriteString(": ")
		value.WriteString(message.Content)
		value.WriteByte('\n')
	}
	return value.String()
}

func conversationCompactionPrompt(previousSummary string, messages []core.Message, files core.ConversationFileContext, customInstructions string) string {
	var value strings.Builder
	if strings.TrimSpace(previousSummary) != "" {
		value.WriteString("<previous-summary>\n")
		value.WriteString(strings.TrimSpace(previousSummary))
		value.WriteString("\n</previous-summary>\n\n")
	}
	value.WriteString("<conversation>\n")
	value.WriteString(renderConversationContext("", messages))
	value.WriteString("</conversation>\n")
	value.WriteString("\n<read-files>\n")
	for _, path := range files.ReadFiles {
		value.WriteString(path)
		value.WriteByte('\n')
	}
	value.WriteString("</read-files>\n")
	value.WriteString("<modified-files>\n")
	for _, path := range files.ModifiedFiles {
		value.WriteString(path)
		value.WriteByte('\n')
	}
	value.WriteString("</modified-files>\n")
	if strings.TrimSpace(previousSummary) == "" {
		value.WriteString("\n")
		value.WriteString(conversationCompactionInitialPrompt)
	} else {
		value.WriteString("\n")
		value.WriteString(conversationCompactionUpdatePrompt)
	}
	if strings.TrimSpace(customInstructions) != "" {
		value.WriteString("\n<additional-instructions>\n")
		value.WriteString(strings.TrimSpace(customInstructions))
		value.WriteString("\n</additional-instructions>\n")
	}
	return value.String()
}

func validConversationCompactionSummary(summary string) bool {
	headings := []string{"## Goal", "## Constraints & Preferences", "## Progress", "## Key Decisions", "## Next Steps", "## Critical Context"}
	position := -1
	for _, heading := range headings {
		next := strings.Index(summary[position+1:], heading)
		if next < 0 {
			return false
		}
		position += next + 1
	}
	return true
}

const conversationCompactionSystemPrompt = `You are a context summarization assistant. Your task is to read a conversation between a user and an AI assistant, then produce a structured summary following the exact format specified.

Do NOT continue the conversation. Do NOT respond to any questions in the conversation. ONLY output the structured summary.`

const conversationCompactionInitialPrompt = `The messages above are a conversation to summarize. Create a structured context checkpoint summary that another LLM will use to continue the work.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

const conversationCompactionUpdatePrompt = `The messages above are NEW conversation messages to incorporate into the existing summary provided in <previous-summary> tags.

Update the existing structured summary with new information. RULES:
- PRESERVE all existing information from the previous summary
- ADD new progress, decisions, and context from the new messages
- UPDATE the Progress section: move items from "In Progress" to "Done" when completed
- UPDATE "Next Steps" based on what was accomplished
- PRESERVE exact file paths, function names, and error messages
- If something is no longer relevant, you may remove it

Use this EXACT format:

## Goal
[Preserve existing goals, add new ones if the task expanded]

## Constraints & Preferences
- [Preserve existing, add new ones discovered]

## Progress
### Done
- [x] [Include previously done items AND newly completed items]

### In Progress
- [ ] [Current work - update based on progress]

### Blocked
- [Current blockers - remove if resolved]

## Key Decisions
- **[Decision]**: [Brief rationale] (preserve all previous, add new)

## Next Steps
1. [Update based on current state]

## Critical Context
- [Preserve important context, add new if needed]

Keep each section concise. Preserve exact file paths, function names, and error messages.`

func parseCompactCommand(content string) (string, bool) {
	parts := strings.SplitN(strings.TrimSpace(content), " ", 2)
	if len(parts) == 0 || !strings.EqualFold(parts[0], "/compact") {
		return "", false
	}
	if len(parts) == 1 {
		return "", true
	}
	return strings.TrimSpace(parts[1]), true
}
