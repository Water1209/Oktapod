package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/bootstrap"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

type contextConfigRepository interface {
	GetConversationContextSettings(context.Context, core.Scope) (core.ConversationContextSettings, error)
	UpsertConversationContextSettings(context.Context, core.Scope, core.UpdateConversationContextSettingsInput) (core.ConversationContextSettings, error)
}

func runContext(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("context subcommand is required; use `context configure` or `context status`")
	}
	config, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, actor, organization, err := openModelConfigStore(ctx, config)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		return err
	}
	scope := core.Scope{OrganizationID: organization.ID, UserID: actor.ID}
	switch args[0] {
	case "configure":
		return runContextConfigure(ctx, store, scope, args[1:], os.Stdout)
	case "status":
		return runContextStatus(ctx, store, scope, args[1:], os.Stdout)
	default:
		return fmt.Errorf("unsupported context subcommand %q; use configure or status", args[0])
	}
}

func runContextConfigure(ctx context.Context, repository contextConfigRepository, scope core.Scope, args []string, output io.Writer) error {
	current, err := repository.GetConversationContextSettings(ctx, scope)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("context configure", flag.ContinueOnError)
	autoCompact := flags.Bool("auto", current.AutoCompact, "automatically compact when the trigger is exceeded")
	triggerTokens := flags.Int64("trigger-tokens", current.TriggerTokens, "estimated context tokens that trigger compaction")
	retainTokens := flags.Int64("retain-tokens", current.RetainTokens, "recent context token budget retained verbatim")
	summaryTokens := flags.Int64("summary-tokens", current.SummaryMaxTokens, "maximum summary output tokens")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("context configure does not accept positional arguments")
	}
	updated, err := repository.UpsertConversationContextSettings(ctx, scope, core.UpdateConversationContextSettingsInput{
		AutoCompact: *autoCompact, TriggerTokens: *triggerTokens, RetainTokens: *retainTokens, SummaryMaxTokens: *summaryTokens,
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "会话上下文配置已保存到数据库并立即生效。\n自动压缩: %s\n触发阈值: %d Token\n保留上下文: %d Token\n摘要上限: %d Token\n", enabledLabel(updated.AutoCompact), updated.TriggerTokens, updated.RetainTokens, updated.SummaryMaxTokens)
	return nil
}

func runContextStatus(ctx context.Context, repository contextConfigRepository, scope core.Scope, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("context status", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("context status does not accept positional arguments")
	}
	settings, err := repository.GetConversationContextSettings(ctx, scope)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "自动压缩: %s\n触发阈值: %d Token\n保留上下文: %d Token\n摘要上限: %d Token\n手动命令: /compact [附加摘要要求]\n", enabledLabel(settings.AutoCompact), settings.TriggerTokens, settings.RetainTokens, settings.SummaryMaxTokens)
	return nil
}

func enabledLabel(value bool) string {
	if value {
		return "已启用"
	}
	return "已停用"
}

var _ contextConfigRepository = (application.Repository)(nil)
