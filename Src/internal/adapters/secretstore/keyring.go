package secretstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/zalando/go-keyring"
)

const serviceName = "Oktapod"

type Keyring struct{}

func NewKeyring() *Keyring { return &Keyring{} }

func ModelCredentialRef(organizationID, credentialID string) string {
	return "model-provider/" + strings.TrimSpace(organizationID) + "/" + strings.TrimSpace(credentialID)
}

func (s *Keyring) Get(_ context.Context, ref string) (string, error) {
	value, err := keyring.Get(serviceName, strings.TrimSpace(ref))
	if err != nil {
		return "", err
	}
	return value, nil
}

func (s *Keyring) Set(_ context.Context, ref, value string) error {
	if strings.TrimSpace(ref) == "" || strings.TrimSpace(value) == "" {
		return fmt.Errorf("credential reference and value are required")
	}
	return keyring.Set(serviceName, strings.TrimSpace(ref), strings.TrimSpace(value))
}

func (s *Keyring) Delete(_ context.Context, ref string) error {
	return keyring.Delete(serviceName, strings.TrimSpace(ref))
}
