package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	postgresdb "github.com/oktapod/oktapod/Src/internal/adapters/db/postgres"
	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/adapters/secretstore"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/bootstrap"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	"golang.org/x/term"
)

type modelSecretStore interface {
	Get(context.Context, string) (string, error)
	Set(context.Context, string, string) error
	Delete(context.Context, string) error
}

type modelConfigRepository interface {
	GetModelProviderConfig(context.Context, string) (core.ModelProviderConfig, error)
	UpsertModelProviderConfig(context.Context, string, core.UpsertModelProviderConfigInput) (core.ModelProviderConfig, error)
	DeleteModelProviderConfig(context.Context, string) error
}

func runModel(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("model subcommand is required; use `model configure` or `model status`")
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

	switch args[0] {
	case "configure":
		return runModelConfigure(ctx, store, secretstore.NewKeyring(), organization.ID, args[1:])
	case "status":
		return runModelStatus(ctx, store, secretstore.NewKeyring(), organization.ID, args[1:])
	case "reset":
		return runModelReset(ctx, store, secretstore.NewKeyring(), organization.ID, args[1:])
	default:
		return fmt.Errorf("unsupported model subcommand %q; use configure, status, or reset", args[0])
	}
}

func openModelConfigStore(ctx context.Context, config bootstrap.Config) (interface {
	application.Repository
	io.Closer
}, core.Actor, core.Organization, error) {
	actor := core.Actor{ID: personalUserID, DisplayName: "本地用户"}
	organization := core.Organization{ID: personalOrgID, Name: "个人空间"}
	if config.Profile == core.ProfileTeam {
		actor = core.Actor{ID: teamUserID, DisplayName: "Team 管理员"}
		organization = core.Organization{ID: teamOrgID, Name: "本地 Team"}
	}
	var store interface {
		application.Repository
		io.Closer
	}
	var err error
	if config.Profile == core.ProfileTeam && strings.TrimSpace(config.DatabaseURL) != "" {
		store, err = postgresdb.Open(ctx, config.DatabaseURL, config.WorkspaceRoot)
	} else {
		store, err = sqlite.Open(ctx, config.DatabasePath, config.WorkspaceRoot)
	}
	return store, actor, organization, err
}

func runModelConfigure(ctx context.Context, repository modelConfigRepository, secrets modelSecretStore, organizationID string, args []string) error {
	flags := flag.NewFlagSet("model configure", flag.ContinueOnError)
	endpoint := flags.String("endpoint", "", "OpenAI-compatible API endpoint")
	model := flags.String("model", "", "default model ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	validatedEndpoint, err := validateModelEndpoint(*endpoint)
	if err != nil {
		return err
	}
	modelID := strings.TrimSpace(*model)
	if modelID == "" {
		return fmt.Errorf("--model is required")
	}
	apiKey, err := readSecret(os.Stdin, os.Stderr)
	if err != nil {
		return err
	}
	secretRef := ""
	existing, existingErr := repository.GetModelProviderConfig(ctx, organizationID)
	if existingErr == nil {
		secretRef = existing.SecretRef
	} else if !errors.Is(existingErr, sql.ErrNoRows) {
		return existingErr
	}
	if secretRef == "" {
		secretRef = secretstore.ModelCredentialRef(organizationID, uuid.NewString())
	}
	if err := saveModelConfig(ctx, repository, secrets, organizationID, secretRef, validatedEndpoint, modelID, apiKey); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "模型配置已保存到数据库，凭据已保存到系统凭据库；重启服务后生效。")
	return nil
}

func saveModelConfig(ctx context.Context, repository modelConfigRepository, secrets modelSecretStore, organizationID, ref, endpoint, modelID, apiKey string) error {
	oldSecret, oldSecretErr := secrets.Get(ctx, ref)
	if err := secrets.Set(ctx, ref, apiKey); err != nil {
		return fmt.Errorf("store model credential in the operating-system credential store: %w", err)
	}
	_, err := repository.UpsertModelProviderConfig(ctx, organizationID, core.UpsertModelProviderConfigInput{
		Endpoint: endpoint, DefaultModel: modelID, SecretRef: ref,
	})
	if err != nil {
		if oldSecretErr == nil {
			_ = secrets.Set(ctx, ref, oldSecret)
		} else {
			_ = secrets.Delete(ctx, ref)
		}
		return err
	}
	return nil
}

func runModelReset(ctx context.Context, repository modelConfigRepository, secrets modelSecretStore, organizationID string, args []string) error {
	flags := flag.NewFlagSet("model reset", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("model reset does not accept positional arguments")
	}
	config, err := repository.GetModelProviderConfig(ctx, organizationID)
	if errors.Is(err, sql.ErrNoRows) {
		fmt.Fprintln(os.Stdout, "模型配置已是未配置状态。")
		return nil
	}
	if err != nil {
		return err
	}
	if err := repository.DeleteModelProviderConfig(ctx, organizationID); err != nil {
		return err
	}
	if err := secrets.Delete(ctx, config.SecretRef); err != nil {
		return fmt.Errorf("database config removed, but credential cleanup failed: %w", err)
	}
	fmt.Fprintln(os.Stdout, "模型配置和系统凭据已删除。")
	return nil
}

func runModelStatus(ctx context.Context, repository modelConfigRepository, secrets modelSecretStore, organizationID string, args []string) error {
	flags := flag.NewFlagSet("model status", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("model status does not accept positional arguments")
	}
	config, err := repository.GetModelProviderConfig(ctx, organizationID)
	if errors.Is(err, sql.ErrNoRows) {
		fmt.Fprintln(os.Stdout, "配置状态: 未配置")
		return nil
	}
	if err != nil {
		return err
	}
	_, credentialErr := secrets.Get(ctx, config.SecretRef)
	fmt.Fprintf(os.Stdout, "配置状态: 已配置\nEndpoint: %s\n默认模型: %s\n凭据状态: %s\n", config.Endpoint, config.DefaultModel, credentialStatus(credentialErr))
	return nil
}

func credentialStatus(err error) string {
	if err == nil {
		return "可用"
	}
	return "缺失或不可访问"
}

func validateModelEndpoint(raw string) (string, error) {
	endpoint := strings.TrimRight(strings.TrimSpace(raw), "/")
	parsed, err := url.Parse(endpoint)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("--endpoint must be an absolute HTTP(S) URL without credentials or a fragment")
	}
	return endpoint, nil
}

func readSecret(input *os.File, prompt io.Writer) (string, error) {
	var value string
	if term.IsTerminal(int(input.Fd())) {
		fmt.Fprint(prompt, "API Key（输入不会回显）: ")
		data, err := term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return "", fmt.Errorf("read API key: %w", err)
		}
		value = string(data)
	} else {
		reader := bufio.NewReader(io.LimitReader(input, 4097))
		data, err := io.ReadAll(reader)
		if err != nil {
			return "", fmt.Errorf("read API key from stdin: %w", err)
		}
		if len(data) > 4096 {
			return "", fmt.Errorf("API key exceeds 4096 bytes")
		}
		value = string(data)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("API key is required")
	}
	return value, nil
}
