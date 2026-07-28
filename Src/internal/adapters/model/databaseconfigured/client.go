package databaseconfigured

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/model/native"
	"github.com/oktapod/oktapod/Src/internal/adapters/model/openaicompat"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
	okruntime "github.com/oktapod/oktapod/Src/internal/runtime"
)

type ConfigRepository interface {
	GetModelProviderConfig(context.Context, string) (core.ModelProviderConfig, error)
}

type providerConfigRepository interface {
	GetProviderInstance(context.Context, string, string) (core.ProviderInstance, error)
	GetDefaultProviderInstance(context.Context, string) (core.ProviderInstance, error)
}

type CredentialStore interface {
	Get(context.Context, string) (string, error)
}

// Client makes the database the live source of truth. A CLI update becomes
// visible to the next model operation without restarting the service.
type Client struct {
	repository     ConfigRepository
	credentials    CredentialStore
	organizationID string

	mu       sync.Mutex
	cacheKey string
	client   configuredClient
}

type configuredClient interface {
	Complete(context.Context, okruntime.ModelRequest) (core.ModelResult, error)
	Stream(context.Context, okruntime.ModelRequest, func(string) error) (core.ModelResult, error)
	ListModels(context.Context) ([]core.ModelDescriptor, error)
	DefaultModel(context.Context) (string, error)
}

func New(repository ConfigRepository, credentials CredentialStore, organizationID string) *Client {
	return &Client{repository: repository, credentials: credentials, organizationID: organizationID}
}

func (c *Client) configured(ctx context.Context, requested core.ModelRef) (configuredClient, core.ModelRef, error) {
	config, err := c.resolveConfig(ctx, requested)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, core.ModelRef{}, &openaicompat.GatewayError{Code: "MODEL_CREDENTIAL_MISSING", Message: "model configuration is absent from the database; run `oktapod model configure`"}
	}
	if err != nil {
		return nil, core.ModelRef{}, fmt.Errorf("read model configuration from database: %w", err)
	}
	effective := core.ModelRef{ProviderID: config.ProviderID, ModelID: config.DefaultModel}
	key := config.ProtocolFamily + "\x00" + config.Endpoint + "\x00" + effective.ProviderID + "\x00" + effective.ModelID + "\x00" + config.SecretRef + "\x00" + config.UpdatedAt.UTC().String()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.client != nil && c.cacheKey == key {
		return c.client, effective, nil
	}
	secretRef := config.SecretRef
	credential := func() string {
		value, credentialErr := c.credentials.Get(context.Background(), secretRef)
		if credentialErr != nil {
			return ""
		}
		return value
	}
	switch config.ProtocolFamily {
	case core.ProviderProtocolAnthropic, core.ProviderProtocolGemini:
		c.client = native.New(config.ProtocolFamily, config.Endpoint, effective.ModelID, credential)
	default:
		c.client = openaicompat.New(config.Endpoint, effective.ModelID, credential)
	}
	c.cacheKey = key
	return c.client, effective, nil
}

type resolvedConfig struct {
	ProviderID     string
	ProtocolFamily string
	Endpoint       string
	DefaultModel   string
	SecretRef      string
	UpdatedAt      time.Time
}

func (c *Client) resolveConfig(ctx context.Context, requested core.ModelRef) (resolvedConfig, error) {
	if repository, ok := c.repository.(providerConfigRepository); ok {
		var provider core.ProviderInstance
		var err error
		if strings.TrimSpace(requested.ProviderID) != "" {
			provider, err = repository.GetProviderInstance(ctx, c.organizationID, requested.ProviderID)
		} else {
			provider, err = repository.GetDefaultProviderInstance(ctx, c.organizationID)
		}
		if err == nil {
			if provider.Status != core.ProviderStatusActive {
				return resolvedConfig{}, fmt.Errorf("provider %s is not active", provider.ProviderID)
			}
			modelID := strings.TrimSpace(requested.ModelID)
			if modelID == "" {
				modelID = provider.DefaultModelID
			}
			return resolvedConfig{
				ProviderID: provider.ProviderID, ProtocolFamily: provider.ProtocolFamily, Endpoint: provider.Endpoint,
				DefaultModel: modelID, SecretRef: provider.CredentialRef, UpdatedAt: provider.UpdatedAt,
			}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return resolvedConfig{}, err
		}
	}
	legacy, err := c.repository.GetModelProviderConfig(ctx, c.organizationID)
	if err != nil {
		return resolvedConfig{}, err
	}
	modelID := strings.TrimSpace(requested.ModelID)
	if modelID == "" {
		modelID = legacy.DefaultModel
	}
	return resolvedConfig{
		ProtocolFamily: core.ProviderProtocolOpenAICompatible, Endpoint: legacy.Endpoint, DefaultModel: modelID,
		SecretRef: legacy.SecretRef, UpdatedAt: legacy.UpdatedAt,
	}, nil
}

func (c *Client) Complete(ctx context.Context, request okruntime.ModelRequest) (core.ModelResult, error) {
	client, effective, err := c.configured(ctx, request.RequestedModel)
	if err != nil {
		return core.ModelResult{ErrorCode: "MODEL_CREDENTIAL_MISSING"}, err
	}
	result, err := client.Complete(ctx, request)
	result.RequestedModel = request.RequestedModel
	if result.RequestedModel.ProviderID == "" {
		result.RequestedModel.ProviderID = effective.ProviderID
	}
	if result.RequestedModel.ModelID == "" {
		result.RequestedModel.ModelID = effective.ModelID
	}
	result.EffectiveModel.ProviderID = effective.ProviderID
	if result.EffectiveModel.ModelID == "" {
		result.EffectiveModel.ModelID = result.Model
	}
	return result, err
}

func (c *Client) Stream(ctx context.Context, request okruntime.ModelRequest, onDelta func(string) error) (core.ModelResult, error) {
	client, effective, err := c.configured(ctx, request.RequestedModel)
	if err != nil {
		return core.ModelResult{ErrorCode: "MODEL_CREDENTIAL_MISSING"}, err
	}
	result, err := client.Stream(ctx, request, onDelta)
	result.RequestedModel = request.RequestedModel
	if result.RequestedModel.ProviderID == "" {
		result.RequestedModel.ProviderID = effective.ProviderID
	}
	if result.RequestedModel.ModelID == "" {
		result.RequestedModel.ModelID = effective.ModelID
	}
	result.EffectiveModel.ProviderID = effective.ProviderID
	if result.EffectiveModel.ModelID == "" {
		result.EffectiveModel.ModelID = result.Model
	}
	return result, err
}

func (c *Client) ListModels(ctx context.Context) ([]core.ModelDescriptor, error) {
	client, _, err := c.configured(ctx, core.ModelRef{})
	if err != nil {
		return nil, err
	}
	return client.ListModels(ctx)
}

func (c *Client) DefaultModel(ctx context.Context) (string, error) {
	client, _, err := c.configured(ctx, core.ModelRef{})
	if err != nil {
		return "", err
	}
	return client.DefaultModel(ctx)
}
