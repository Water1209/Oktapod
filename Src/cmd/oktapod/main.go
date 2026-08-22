package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	localblob "github.com/oktapod/oktapod/Src/internal/adapters/blob/localfs"
	postgresdb "github.com/oktapod/oktapod/Src/internal/adapters/db/postgres"
	"github.com/oktapod/oktapod/Src/internal/adapters/db/sqlite"
	"github.com/oktapod/oktapod/Src/internal/adapters/httpapi"
	mcpadapter "github.com/oktapod/oktapod/Src/internal/adapters/mcp"
	"github.com/oktapod/oktapod/Src/internal/adapters/mcpregistry"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/databaseconfigured"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/native"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/adapters/secretstore"
	"github.com/oktapod/oktapod/Src/internal/adapters/webaccess"
	"github.com/oktapod/oktapod/Src/internal/adapters/websearch"
	"github.com/oktapod/oktapod/Src/internal/adapters/workspace"
	"github.com/oktapod/oktapod/Src/internal/application"
	"github.com/oktapod/oktapod/Src/internal/bootstrap"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
	webui "github.com/oktapod/oktapod/Src/web"
)

const (
	personalUserID = "00000000-0000-7000-8000-000000000001"
	personalOrgID  = "00000000-0000-7000-8000-000000000002"
	teamUserID     = "00000000-0000-7000-8000-000000000011"
	teamOrgID      = "00000000-0000-7000-8000-000000000012"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	command, commandArgs, err := parseCommandLine(os.Args[1:])
	if err != nil {
		return err
	}
	if command == "backup" {
		return runBackup(commandArgs)
	}
	if command == "preflight" {
		return runPreflight(commandArgs)
	}
	if command == "cleanup" {
		return runCleanup(commandArgs)
	}
	if command == "restore" {
		return runRestore(commandArgs)
	}
	if command == "export" {
		return runProjectExport(commandArgs)
	}
	if command == "import" {
		return runProjectImport(commandArgs)
	}
	if command == "model" {
		return runModel(commandArgs)
	}
	if command == "context" {
		return runContext(commandArgs)
	}
	if command == "config" {
		return runConfig(commandArgs)
	}
	if command == "vault" {
		return runVault(commandArgs)
	}
	if command == "version" {
		fmt.Fprintln(os.Stdout, application.RuntimeVersion)
		return nil
	}
	if command != "serve" {
		return fmt.Errorf("unsupported command %q; use serve, model, context, config, vault, preflight, backup, restore, export, import, or version", command)
	}

	config, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if config.Profile == core.ProfileTeam && strings.TrimSpace(config.DatabaseURL) == "" {
		logger.Warn("team profile is running with local SQLite storage; PostgreSQL identity/RLS is not enabled", "storage_mode", "sqlite-local", "network_exposure", config.ListenAddress)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	var store interface {
		application.Repository
		secretstore.VaultRepository
		io.Closer
	}
	if config.Profile == core.ProfileTeam && strings.TrimSpace(config.DatabaseURL) != "" {
		store, err = postgresdb.Open(ctx, config.DatabaseURL, config.WorkspaceRoot)
	} else {
		store, err = sqlite.Open(ctx, config.DatabasePath, config.WorkspaceRoot)
	}
	if err != nil {
		return err
	}
	defer store.Close()
	if configurable, ok := store.(interface {
		SetExecutionPolicy(application.ExecutionPolicy)
	}); ok {
		configurable.SetExecutionPolicy(application.StaticExecutionPolicy{WebAccess: config.WebAccessPolicy})
	}

	actor := core.Actor{ID: personalUserID, DisplayName: "本地用户"}
	organization := core.Organization{ID: personalOrgID, Name: "个人空间"}
	if config.Profile == core.ProfileTeam {
		actor = core.Actor{ID: teamUserID, DisplayName: "Team 管理员"}
		organization = core.Organization{ID: teamOrgID, Name: "本地 Team"}
	}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		return err
	}
	configManager := bootstrap.NewConfigManager(config)
	vaultKeys, err := secretstore.NewVaultKeyManager(ctx, store, organization.ID, config.DataRoot)
	if err != nil {
		return err
	}
	credentials := secretstore.NewDatabase(store, vaultKeys, organization.ID)
	modelEndpoint := bootstrap.DefaultModelEndpoint
	modelConfigured := false
	providerConfig, providerErr := store.GetModelProviderConfig(ctx, organization.ID)
	if providerErr == nil {
		modelEndpoint = providerConfig.Endpoint
		_, credentialErr := credentials.Get(ctx, providerConfig.SecretRef)
		modelConfigured = credentialErr == nil
	} else if !errors.Is(providerErr, sql.ErrNoRows) {
		return fmt.Errorf("load model provider config: %w", providerErr)
	}
	model := databaseconfigured.New(store, credentials, organization.ID)
	service := application.NewService(store, config.Profile, modelEndpoint, actor, organization, func() string {
		provider, err := store.GetModelProviderConfig(context.Background(), organization.ID)
		if err != nil {
			return ""
		}
		value, err := credentials.Get(context.Background(), provider.SecretRef)
		if err != nil {
			return ""
		}
		return value
	})
	service.WithVault(vaultKeys).WithBootstrapConfiguration(configManager).WithWebAccessPolicy(config.WebAccessPolicy)
	service.WithMCPRegistry(mcpregistry.New(nil))
	trustedCapabilityKeys, keyErr := bootstrap.CapabilityTrustedKeys(config.CapabilityTrustedKeysJSON)
	if keyErr != nil {
		return keyErr
	}
	service.WithCapabilityTrustedKeys(trustedCapabilityKeys)
	if config.Profile == core.ProfileTeam && strings.TrimSpace(config.DatabaseURL) != "" {
		service.WithDatabase("postgresql")
	}
	service.WithAdvisorModel(advisorGateway{model: model})
	service.WithModelCatalog(model)
	service.WithProviderRegistry(credentials, func(provider core.ProviderInstance, credential string) application.ModelCatalog {
		if provider.ProtocolFamily == core.ProviderProtocolAnthropic || provider.ProtocolFamily == core.ProviderProtocolGemini {
			return native.New(provider.ProtocolFamily, provider.Endpoint, provider.DefaultModelID, func() string { return credential })
		}
		return openaicompat.New(provider.Endpoint, provider.DefaultModelID, func() string { return credential })
	})
	oauthCoordinator, err := mcpadapter.NewOAuthCoordinator(ctx, credentials, mcpOAuthRedirectURL(config.ListenAddress))
	if err != nil {
		return err
	}
	service.WithMCPOAuthCoordinator(oauthCoordinator)
	workspaceExecutor, err := workspace.NewLocal(config.WorkspaceRoot)
	if err != nil {
		return err
	}
	service.WithChatToolExecutor(workspaceExecutor, config.WorkspaceRoot)
	blobStore, err := localblob.New(filepath.Join(filepath.Dir(config.WorkspaceRoot), "blobs"))
	if err != nil {
		return err
	}
	artifactCapture, err := application.NewArtifactCaptureService(workspaceExecutor, blobStore)
	if err != nil {
		return err
	}
	service.WithArtifactCapture(artifactCapture)
	webSearchExecutor, err := websearch.New("")
	if err != nil {
		return err
	}
	webAccessExecutor, err := webaccess.New(webSearchExecutor, config.WebAccessPolicy)
	if err != nil {
		return err
	}
	queuedTurnStarter := func(ctx context.Context, scope core.Scope, item core.ConversationQueueItem) (core.Task, error) {
		return service.StartQueuedConversationTurn(ctx, item)
	}
	liveTurnHub := okruntime.NewLiveTurnHub()
	workerCtx := application.WithScope(ctx, service.Scope())
	hostname, _ := os.Hostname()
	workerInstanceID := fmt.Sprintf("%s-%d-%d", hostname, os.Getpid(), time.Now().UnixNano())
	var workerGroup sync.WaitGroup
	for index := 0; index < config.RunConcurrency; index++ {
		workerID := fmt.Sprintf("%s-%d", workerInstanceID, index+1)
		worker := okruntime.NewWorker(workerID, store, model, logger).
			WithWorkspaceExecutor(workspaceExecutor).
			WithArtifactCapture(artifactCapture).
			WithAssetStager(workspaceExecutor).
			WithWebAccess(webAccessExecutor).
			WithMCPSecretStore(credentials).
			WithQueuedTurnStarter(queuedTurnStarter).
			WithLiveTurnPublisher(liveTurnHub)
		workerGroup.Add(1)
		go func() {
			defer workerGroup.Done()
			if workerErr := worker.Run(workerCtx); workerErr != nil && !errors.Is(workerErr, context.Canceled) {
				logger.Error("worker stopped", "worker_id", workerID, "error", workerErr)
				cancel()
			}
		}()
	}
	scheduler := okruntime.NewScheduler(store, logger)
	go func() {
		if schedulerErr := scheduler.Run(workerCtx); schedulerErr != nil && !errors.Is(schedulerErr, context.Canceled) {
			logger.Error("scheduler stopped", "error", schedulerErr)
		}
	}()
	go func() {
		if summaryErr := okruntime.RunDailySummaryLoop(workerCtx, service, logger); summaryErr != nil && !errors.Is(summaryErr, context.Canceled) {
			logger.Error("daily summary runner stopped", "error", summaryErr)
		}
	}()

	handler := httpapi.NewHandler(service, logger, liveTurnHub)
	if ui, ok := webui.Handler(); ok {
		handler = httpapi.NewHandlerWithUI(service, logger, ui, liveTurnHub)
	}
	httpServer := &http.Server{
		Addr:              config.ListenAddress,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("Oktapod started", "version", application.RuntimeVersion, "profile", config.Profile, "listen", config.ListenAddress, "data_root", config.DataRoot, "model_endpoint", modelEndpoint, "model_configured", modelConfigured)
		serverErrors <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
	case serverErr := <-serverErrors:
		if !errors.Is(serverErr, http.ErrServerClosed) {
			return serverErr
		}
	}
	cancel()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	shutdownErr := httpServer.Shutdown(shutdownCtx)
	workersDone := make(chan struct{})
	go func() {
		workerGroup.Wait()
		close(workersDone)
	}()
	select {
	case <-workersDone:
	case <-shutdownCtx.Done():
		logger.Warn("worker shutdown timed out; durable leases will recover unfinished work")
	}
	return shutdownErr
}

// parseCommandLine removes the global --config option before subcommand flag
// parsing. LoadConfig still reads it from os.Args, so the option has the same
// precedence whether it appears before or after the subcommand.
func parseCommandLine(args []string) (string, []string, error) {
	filtered := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--config" {
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return "", nil, fmt.Errorf("--config requires a path")
			}
			index++
			continue
		}
		if strings.HasPrefix(argument, "--config=") {
			if strings.TrimSpace(strings.TrimPrefix(argument, "--config=")) == "" {
				return "", nil, fmt.Errorf("--config requires a path")
			}
			continue
		}
		filtered = append(filtered, argument)
	}
	if len(filtered) == 0 {
		return "serve", nil, nil
	}
	return filtered[0], filtered[1:], nil
}

func mcpOAuthRedirectURL(listenAddress string) string {
	_, port, err := net.SplitHostPort(strings.TrimSpace(listenAddress))
	if err != nil || strings.TrimSpace(port) == "" {
		port = "8787"
	}
	return "http://127.0.0.1:" + port + "/api/v1/mcp/oauth/callback"
}

func runPreflight(args []string) error {
	flags := flag.NewFlagSet("preflight", flag.ContinueOnError)
	databasePath := flags.String("database", "", "SQLite database path; defaults to the configured Personal database")
	if err := flags.Parse(args); err != nil {
		return err
	}
	config, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	if isPostgresTeam(config) {
		preflightDSN := config.DatabaseBackupURL
		if strings.TrimSpace(preflightDSN) == "" {
			preflightDSN = config.DatabaseURL
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		report, preflightErr := postgresdb.Preflight(ctx, preflightDSN)
		if preflightErr != nil {
			return preflightErr
		}
		data, marshalErr := json.MarshalIndent(report, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		fmt.Fprintln(os.Stdout, string(data))
		if !report.Safe {
			return fmt.Errorf("upgrade preflight is not safe")
		}
		return nil
	}
	if strings.TrimSpace(config.DatabaseURL) != "" {
		return fmt.Errorf("preflight database URL is only supported for the Team profile")
	}
	path := strings.TrimSpace(*databasePath)
	if path == "" {
		path = config.DatabasePath
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := sqlite.PreflightSQLite(ctx, path)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, string(data))
	if !report.Safe {
		return fmt.Errorf("upgrade preflight is not safe")
	}
	return nil
}

type advisorGateway struct {
	model interface {
		Complete(context.Context, okruntime.ModelRequest) (core.ModelResult, error)
		Stream(context.Context, okruntime.ModelRequest, func(string) error) (core.ModelResult, error)
	}
}

func (a advisorGateway) Complete(ctx context.Context, systemPrompt, userPrompt string, maxOutputTokens int) (core.ModelResult, error) {
	return a.model.Complete(ctx, okruntime.ModelRequest{SystemPrompt: systemPrompt, UserPrompt: userPrompt, MaxOutputTokens: maxOutputTokens, Model: "", FallbackModel: ""})
}

func (a advisorGateway) CompleteTurn(ctx context.Context, systemPrompt, userPrompt string, maxOutputTokens int, model, fallbackModel string) (core.ModelResult, error) {
	return a.model.Complete(ctx, okruntime.ModelRequest{SystemPrompt: systemPrompt, UserPrompt: userPrompt, MaxOutputTokens: maxOutputTokens, Model: model, FallbackModel: fallbackModel})
}

func (a advisorGateway) CompleteTurnWithModelRef(ctx context.Context, systemPrompt, userPrompt string, maxOutputTokens int, requested core.ModelRef, fallbackModel string) (core.ModelResult, error) {
	return a.model.Complete(ctx, okruntime.ModelRequest{SystemPrompt: systemPrompt, UserPrompt: userPrompt, RequestedModel: requested, MaxOutputTokens: maxOutputTokens, Model: requested.ModelID, FallbackModel: fallbackModel})
}

func (a advisorGateway) CompleteMessages(ctx context.Context, systemPrompt string, messages []core.ModelMessage, maxOutputTokens int, model, fallbackModel string) (core.ModelResult, error) {
	return a.model.Complete(ctx, okruntime.ModelRequest{SystemPrompt: systemPrompt, Messages: messages, MaxOutputTokens: maxOutputTokens, Model: model, FallbackModel: fallbackModel})
}

func (a advisorGateway) CompleteMessagesWithModelRef(ctx context.Context, systemPrompt string, messages []core.ModelMessage, maxOutputTokens int, requested core.ModelRef, fallbackModel string) (core.ModelResult, error) {
	return a.model.Complete(ctx, okruntime.ModelRequest{SystemPrompt: systemPrompt, Messages: messages, RequestedModel: requested, MaxOutputTokens: maxOutputTokens, Model: requested.ModelID, FallbackModel: fallbackModel})
}

func (a advisorGateway) StreamTurn(ctx context.Context, systemPrompt, userPrompt string, maxOutputTokens int, model, fallbackModel string, onDelta func(string) error) (core.ModelResult, error) {
	return a.model.Stream(ctx, okruntime.ModelRequest{SystemPrompt: systemPrompt, UserPrompt: userPrompt, MaxOutputTokens: maxOutputTokens, Model: model, FallbackModel: fallbackModel}, onDelta)
}

func (a advisorGateway) StreamMessages(ctx context.Context, systemPrompt string, messages []core.ModelMessage, maxOutputTokens int, model, fallbackModel string, onDelta func(string) error) (core.ModelResult, error) {
	return a.model.Stream(ctx, okruntime.ModelRequest{SystemPrompt: systemPrompt, Messages: messages, MaxOutputTokens: maxOutputTokens, Model: model, FallbackModel: fallbackModel}, onDelta)
}

func (a advisorGateway) StreamMessagesWithModelRef(ctx context.Context, systemPrompt string, messages []core.ModelMessage, maxOutputTokens int, requested core.ModelRef, fallbackModel string, onDelta func(string) error) (core.ModelResult, error) {
	return a.model.Stream(ctx, okruntime.ModelRequest{SystemPrompt: systemPrompt, Messages: messages, RequestedModel: requested, MaxOutputTokens: maxOutputTokens, Model: requested.ModelID, FallbackModel: fallbackModel}, onDelta)
}

func (a advisorGateway) StreamMessagesWithTools(ctx context.Context, systemPrompt string, messages []core.ModelMessage, maxOutputTokens int, requested core.ModelRef, fallbackModel string, toolDefinitions []application.ChatToolDefinition, onDelta func(string) error) (core.ModelResult, error) {
	runtimeDefs := make([]okruntime.ToolDefinition, 0, len(toolDefinitions))
	for _, def := range toolDefinitions {
		runtimeDefs = append(runtimeDefs, okruntime.ToolDefinition{Name: def.Name, Description: def.Description, Parameters: def.Parameters})
	}
	return a.model.Stream(ctx, okruntime.ModelRequest{
		SystemPrompt: systemPrompt, Messages: messages, RequestedModel: requested,
		MaxOutputTokens: maxOutputTokens, Model: requested.ModelID, FallbackModel: fallbackModel,
		EnableTools: true, ToolDefinitions: runtimeDefs,
	}, onDelta)
}

func runBackup(args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	output := flags.String("output", "", "empty directory receiving the consistent snapshot")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *output == "" {
		return fmt.Errorf("backup requires -output <directory>")
	}
	config, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	if isPostgresTeam(config) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		store, openErr := postgresdb.Open(ctx, config.DatabaseURL, config.WorkspaceRoot)
		if openErr != nil {
			return openErr
		}
		defer store.Close()
		backupDSN := config.DatabaseBackupURL
		if strings.TrimSpace(backupDSN) == "" {
			backupDSN = config.DatabaseURL
		}
		manifest, backupErr := store.BackupWithDSN(ctx, *output, backupDSN)
		if backupErr != nil {
			return backupErr
		}
		fmt.Fprintf(os.Stdout, "backup created: %s (database=postgresql schema=%d files=%d)\n", *output, manifest.SchemaVersion, len(manifest.Workspaces)+len(manifest.Blobs)+1)
		return nil
	}
	if err := requireSQLiteMaintenance(config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	store, err := sqlite.Open(ctx, config.DatabasePath, config.WorkspaceRoot)
	if err != nil {
		return err
	}
	defer store.Close()
	manifest, err := store.Backup(ctx, *output)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "backup created: %s (schema=%d files=%d)\n", *output, manifest.SchemaVersion, len(manifest.Workspaces)+len(manifest.Blobs)+1)
	return nil
}

func runRestore(args []string) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	input := flags.String("input", "", "backup directory created by oktapod backup")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("restore requires -input <directory>")
	}
	config, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	if isPostgresTeam(config) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		// Restore with the application/database owner so restored objects keep
		// the runtime role's ownership. The backup DSN is read-only/BYPASSRLS
		// maintenance access and is used for dump/preflight, not ownership.
		manifest, restoreErr := postgresdb.Restore(ctx, *input, config.DatabaseURL, config.WorkspaceRoot)
		if restoreErr != nil {
			return restoreErr
		}
		fmt.Fprintf(os.Stdout, "backup restored: %s (database=postgresql schema=%d)\n", *input, manifest.SchemaVersion)
		return nil
	}
	if err := requireSQLiteMaintenance(config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	manifest, err := sqlite.Restore(ctx, *input, config.DatabasePath, config.WorkspaceRoot)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "backup restored: %s (schema=%d)\n", *input, manifest.SchemaVersion)
	return nil
}

func runProjectExport(args []string) error {
	flags := flag.NewFlagSet("export", flag.ContinueOnError)
	projectID := flags.String("project", "", "project ID to export")
	output := flags.String("output", "", "empty directory receiving the logical project archive")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *projectID == "" || *output == "" {
		return fmt.Errorf("export requires -project <id> -output <directory>")
	}
	config, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	if isPostgresTeam(config) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		store, openErr := postgresdb.Open(ctx, config.DatabaseURL, config.WorkspaceRoot)
		if openErr != nil {
			return openErr
		}
		defer store.Close()
		actor, organization := profileIdentity(config.Profile)
		if err := store.Bootstrap(ctx, actor, organization); err != nil {
			return err
		}
		archive, exportErr := store.ExportProject(ctx, core.Scope{OrganizationID: organization.ID, UserID: actor.ID}, *projectID, *output)
		if exportErr != nil {
			return exportErr
		}
		fmt.Fprintf(os.Stdout, "project archive exported: %s (project=%s files=%d blobs=%d skipped_secrets=%d)\n", *output, archive.SourceProjectID, len(archive.Files), len(archive.Blobs), len(archive.SkippedSecrets))
		return nil
	}
	if err := requireSQLiteMaintenance(config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	store, err := sqlite.Open(ctx, config.DatabasePath, config.WorkspaceRoot)
	if err != nil {
		return err
	}
	defer store.Close()
	actor, organization := profileIdentity(config.Profile)
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		return err
	}
	archive, err := store.ExportProject(ctx, core.Scope{OrganizationID: organization.ID, UserID: actor.ID}, *projectID, *output)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "project archive exported: %s (project=%s files=%d blobs=%d skipped_secrets=%d)\n", *output, archive.SourceProjectID, len(archive.Files), len(archive.Blobs), len(archive.SkippedSecrets))
	return nil
}

func runProjectImport(args []string) error {
	flags := flag.NewFlagSet("import", flag.ContinueOnError)
	input := flags.String("input", "", "project archive directory created by oktapod export")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return fmt.Errorf("import requires -input <directory>")
	}
	config, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	if isPostgresTeam(config) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		store, openErr := postgresdb.Open(ctx, config.DatabaseURL, config.WorkspaceRoot)
		if openErr != nil {
			return openErr
		}
		defer store.Close()
		actor, organization := profileIdentity(config.Profile)
		if err := store.Bootstrap(ctx, actor, organization); err != nil {
			return err
		}
		project, archive, importErr := store.ImportProject(ctx, core.Scope{OrganizationID: organization.ID, UserID: actor.ID}, *input)
		if importErr != nil {
			return importErr
		}
		fmt.Fprintf(os.Stdout, "project archive imported: project=%s name=%s source=%s skipped_secrets=%d\n", project.ID, project.Name, archive.SourceProjectID, len(archive.SkippedSecrets))
		return nil
	}
	if err := requireSQLiteMaintenance(config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	store, err := sqlite.Open(ctx, config.DatabasePath, config.WorkspaceRoot)
	if err != nil {
		return err
	}
	defer store.Close()
	actor, organization := profileIdentity(config.Profile)
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		return err
	}
	project, archive, err := store.ImportProject(ctx, core.Scope{OrganizationID: organization.ID, UserID: actor.ID}, *input)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "project archive imported: project=%s name=%s source=%s skipped_secrets=%d\n", project.ID, project.Name, archive.SourceProjectID, len(archive.SkippedSecrets))
	return nil
}

func profileIdentity(profile string) (core.Actor, core.Organization) {
	if profile == core.ProfileTeam {
		return core.Actor{ID: teamUserID, DisplayName: "Team 管理员"}, core.Organization{ID: teamOrgID, Name: "本地 Team"}
	}
	return core.Actor{ID: personalUserID, DisplayName: "本地用户"}, core.Organization{ID: personalOrgID, Name: "个人空间"}
}

func requireSQLiteMaintenance(config bootstrap.Config) error {
	if isPostgresTeam(config) {
		return fmt.Errorf("this maintenance operation requires the PostgreSQL Team path")
	}
	return nil
}

func isPostgresTeam(config bootstrap.Config) bool {
	return config.Profile == core.ProfileTeam && strings.TrimSpace(config.DatabaseURL) != ""
}

func runCleanup(args []string) error {
	flags := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	if err := flags.Parse(args); err != nil {
		return err
	}
	config, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	if isPostgresTeam(config) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		store, openErr := postgresdb.Open(ctx, config.DatabaseURL, config.WorkspaceRoot)
		if openErr != nil {
			return openErr
		}
		defer store.Close()
		actor, organization := profileIdentity(config.Profile)
		if err := store.Bootstrap(ctx, actor, organization); err != nil {
			return err
		}
		report, cleanupErr := store.Cleanup(ctx, core.Scope{OrganizationID: organization.ID, UserID: actor.ID}, time.Now().UTC())
		if cleanupErr != nil {
			return cleanupErr
		}
		fmt.Fprintf(os.Stdout, "cleanup completed: temporary=%d orphan_blobs=%d protected=%d\n", report.DeletedTempFiles, report.DeletedOrphanBlobs, report.SkippedProtected)
		return nil
	}
	if err := requireSQLiteMaintenance(config); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	store, err := sqlite.Open(ctx, config.DatabasePath, config.WorkspaceRoot)
	if err != nil {
		return err
	}
	defer store.Close()
	actor := core.Actor{ID: personalUserID, DisplayName: "本地用户"}
	organization := core.Organization{ID: personalOrgID, Name: "个人空间"}
	if config.Profile == core.ProfileTeam {
		actor = core.Actor{ID: teamUserID, DisplayName: "Team 管理员"}
		organization = core.Organization{ID: teamOrgID, Name: "本地 Team"}
	}
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		return err
	}
	report, err := store.Cleanup(ctx, core.Scope{OrganizationID: organization.ID, UserID: actor.ID}, time.Now().UTC())
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "cleanup completed: temporary=%d orphan_blobs=%d protected=%d\n", report.DeletedTempFiles, report.DeletedOrphanBlobs, report.SkippedProtected)
	return nil
}
