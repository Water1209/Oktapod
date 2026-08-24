package application

import (
	"context"
	"errors"
	"testing"
)

func TestRetryTransientSQLiteValueRetriesBusySnapshot(t *testing.T) {
	attempts := 0
	value, err := retryTransientSQLiteValue(context.Background(), func() (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("database is locked (517)")
		}
		return "ready", nil
	})
	if err != nil {
		t.Fatalf("retry transient SQLite value: %v", err)
	}
	if value != "ready" || attempts != 3 {
		t.Fatalf("value = %q, attempts = %d; want ready after 3 attempts", value, attempts)
	}
}

func TestRetryTransientSQLiteValueDoesNotRetryPermanentError(t *testing.T) {
	attempts := 0
	wantErr := errors.New("constraint failed")
	_, err := retryTransientSQLiteValue(context.Background(), func() (string, error) {
		attempts++
		return "", wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v; want %v", err, wantErr)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d; want 1", attempts)
	}
}
