package application

import (
	"context"
	"fmt"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// ModelCatalog is deliberately a narrow port. The application layer only
// needs provider-advertised ids; transport, credentials and response shaping
// stay in the model adapter.
type ModelCatalog interface {
	ListModels(ctx context.Context) ([]core.ModelDescriptor, error)
}

type DefaultModelCatalog interface {
	DefaultModel(ctx context.Context) (string, error)
}

type ModelCatalogUnavailableError struct {
	Code string
	Err  error
}

func (e *ModelCatalogUnavailableError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *ModelCatalogUnavailableError) Unwrap() error { return e.Err }

type errorCoder interface {
	ErrorCode() string
}

type ModelTurnUnavailableError struct {
	Err error
}

func (e *ModelTurnUnavailableError) Error() string {
	if e.Err == nil {
		return "MODEL_TURN_UNAVAILABLE"
	}
	return fmt.Sprintf("MODEL_TURN_UNAVAILABLE: %v", e.Err)
}

func (e *ModelTurnUnavailableError) Unwrap() error { return e.Err }
