package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

const mcpOAuthCredentialSchemaV1 = "oktapod.mcp-oauth/v1"

// CredentialUpdater persists an opaque OAuth envelope in SecretStore. The
// adapter never receives a database handle and callers never receive tokens.
type CredentialUpdater func(context.Context, string) error

type SessionOptions struct {
	AuthorizationCodeFetcher sdkauth.AuthorizationCodeFetcher
	CredentialUpdater        CredentialUpdater
	RedirectURL              string
}

type oauthCredentialEnvelope struct {
	Schema string              `json:"schema"`
	Config oauthConfigEnvelope `json:"config"`
	Token  oauthTokenEnvelope  `json:"token"`
}

type oauthConfigEnvelope struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret,omitempty"`
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	RedirectURL  string   `json:"redirect_url"`
	Scopes       []string `json:"scopes,omitempty"`
}

type oauthTokenEnvelope struct {
	AccessToken  string    `json:"access_token"`
	TokenType    string    `json:"token_type,omitempty"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	Expiry       time.Time `json:"expiry,omitempty"`
}

func encodeOAuthCredential(config *oauth2.Config, token *oauth2.Token) (string, error) {
	if config == nil || token == nil || strings.TrimSpace(config.ClientID) == "" || strings.TrimSpace(token.AccessToken) == "" {
		return "", errors.New("OAuth session is incomplete")
	}
	envelope := oauthCredentialEnvelope{
		Schema: mcpOAuthCredentialSchemaV1,
		Config: oauthConfigEnvelope{
			ClientID: config.ClientID, ClientSecret: config.ClientSecret,
			AuthURL: config.Endpoint.AuthURL, TokenURL: config.Endpoint.TokenURL,
			RedirectURL: config.RedirectURL, Scopes: append([]string(nil), config.Scopes...),
		},
		Token: oauthTokenEnvelope{
			AccessToken: token.AccessToken, TokenType: token.TokenType,
			RefreshToken: token.RefreshToken, Expiry: token.Expiry,
		},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", fmt.Errorf("encode OAuth session: %w", err)
	}
	return string(encoded), nil
}

func decodeOAuthCredential(supplied string) (*oauth2.Config, *oauth2.Token, bool, error) {
	trimmed := strings.TrimSpace(supplied)
	if trimmed == "" {
		return nil, nil, false, nil
	}
	if strings.HasPrefix(trimmed, "{") {
		var envelope oauthCredentialEnvelope
		if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
			return nil, nil, false, fmt.Errorf("decode OAuth session: %w", err)
		}
		if envelope.Schema == mcpOAuthCredentialSchemaV1 {
			if strings.TrimSpace(envelope.Config.ClientID) == "" || strings.TrimSpace(envelope.Token.AccessToken) == "" {
				return nil, nil, false, errors.New("OAuth session is incomplete")
			}
			config := &oauth2.Config{
				ClientID: envelope.Config.ClientID, ClientSecret: envelope.Config.ClientSecret,
				Endpoint:    oauth2.Endpoint{AuthURL: envelope.Config.AuthURL, TokenURL: envelope.Config.TokenURL},
				RedirectURL: envelope.Config.RedirectURL, Scopes: append([]string(nil), envelope.Config.Scopes...),
			}
			token := &oauth2.Token{
				AccessToken: envelope.Token.AccessToken, TokenType: envelope.Token.TokenType,
				RefreshToken: envelope.Token.RefreshToken, Expiry: envelope.Token.Expiry,
			}
			return config, token, true, nil
		}
		// Preserve the pre-OAuth-flow V1 migration path. Earlier installations
		// stored an access token in the generic named-secret envelope.
		if values, isSet, err := decodeCredentialSet(trimmed); err == nil && isSet {
			if token := strings.TrimSpace(values["credential"]); token != "" {
				return nil, &oauth2.Token{AccessToken: token, TokenType: "Bearer"}, true, nil
			}
		}
		return nil, nil, false, errors.New("OAuth SecretStore payload has an unsupported schema")
	}
	// A raw value is accepted only as a backward-compatible access token. New
	// OAuth installations always use the versioned envelope above.
	return nil, &oauth2.Token{AccessToken: trimmed, TokenType: "Bearer"}, true, nil
}

func newOAuthHandler(httpClient *http.Client, redirectURL, supplied string, options SessionOptions) (*sdkauth.AuthorizationCodeHandler, error) {
	config, token, exists, err := decodeOAuthCredential(supplied)
	if err != nil {
		return nil, err
	}
	if config != nil && strings.TrimSpace(config.RedirectURL) != "" {
		redirectURL = config.RedirectURL
	}
	if strings.TrimSpace(redirectURL) == "" {
		// This value is never navigated when a restored/legacy session is valid;
		// it only satisfies SDK construction so a 401 can become ErrNeedsAuth.
		redirectURL = "http://127.0.0.1/api/v1/mcp/oauth/callback"
	}
	fetcher := options.AuthorizationCodeFetcher
	if fetcher == nil {
		fetcher = func(context.Context, *sdkauth.AuthorizationArgs) (*sdkauth.AuthorizationResult, error) {
			return nil, ErrNeedsAuth
		}
	}
	handlerConfig := &sdkauth.AuthorizationCodeHandlerConfig{
		RedirectURL: redirectURL,
		DynamicClientRegistrationConfig: &sdkauth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			RedirectURIs: []string{redirectURL}, TokenEndpointAuthMethod: "none",
			GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
			ClientName: "Oktapod", SoftwareID: "oktapod", SoftwareVersion: "1.0.0", ApplicationType: "native",
		}},
		AuthorizationCodeFetcher: fetcher,
		RequestRefreshToken:      true,
		Client:                   httpClient,
	}
	if exists && config != nil {
		credentials := &oauthex.ClientCredentials{ClientID: config.ClientID}
		if config.ClientSecret != "" {
			credentials.ClientSecretAuth = &oauthex.ClientSecretAuth{ClientSecret: config.ClientSecret}
		}
		handlerConfig.PreregisteredClient = credentials
		handlerConfig.RedirectURL = config.RedirectURL
		handlerConfig.InitialTokenSource = newSavingTokenSource(config.TokenSource(oauthHTTPContext(httpClient), token), config, token, options.CredentialUpdater)
	} else if exists && token != nil {
		handlerConfig.InitialTokenSource = oauth2.StaticTokenSource(token)
	}
	handlerConfig.NewTokenSource = func(ctx context.Context, config *oauth2.Config, token *oauth2.Token) (oauth2.TokenSource, error) {
		if options.CredentialUpdater != nil {
			encoded, encodeErr := encodeOAuthCredential(config, token)
			if encodeErr != nil {
				return nil, encodeErr
			}
			if saveErr := options.CredentialUpdater(ctx, encoded); saveErr != nil {
				return nil, fmt.Errorf("persist OAuth session: %w", saveErr)
			}
		}
		return newSavingTokenSource(config.TokenSource(ctx, token), config, token, options.CredentialUpdater), nil
	}
	return sdkauth.NewAuthorizationCodeHandler(handlerConfig)
}

func oauthHTTPContext(client *http.Client) context.Context {
	return context.WithValue(context.Background(), oauth2.HTTPClient, client)
}

type savingTokenSource struct {
	mu          sync.Mutex
	source      oauth2.TokenSource
	config      *oauth2.Config
	accessToken string
	updater     CredentialUpdater
}

func newSavingTokenSource(source oauth2.TokenSource, config *oauth2.Config, initial *oauth2.Token, updater CredentialUpdater) oauth2.TokenSource {
	if source == nil || updater == nil {
		return source
	}
	accessToken := ""
	if initial != nil {
		accessToken = initial.AccessToken
	}
	return &savingTokenSource{source: source, config: config, accessToken: accessToken, updater: updater}
}

func (source *savingTokenSource) Token() (*oauth2.Token, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	token, err := source.source.Token()
	if err != nil {
		return nil, err
	}
	if token.AccessToken == source.accessToken {
		return token, nil
	}
	encoded, err := encodeOAuthCredential(source.config, token)
	if err != nil {
		return nil, err
	}
	if err := source.updater(context.Background(), encoded); err != nil {
		return nil, fmt.Errorf("persist refreshed OAuth session: %w", err)
	}
	source.accessToken = token.AccessToken
	return token, nil
}
