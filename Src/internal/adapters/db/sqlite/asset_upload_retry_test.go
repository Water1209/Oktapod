package sqlite

import (
	"context"
	"errors"
	"testing"
)

func TestRetrySQLiteBusyValueRetriesBusySnapshot(t *testing.T) {
	attempts := 0
	value, err := retrySQLiteBusyValue(context.Background(), func() (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("database is locked (SQLITE_BUSY_SNAPSHOT)")
		}
		return "registered", nil
	})
	if err != nil {
		t.Fatalf("retry SQLite upload registration: %v", err)
	}
	if value != "registered" || attempts != 3 {
		t.Fatalf("value = %q, attempts = %d; want registered after 3 attempts", value, attempts)
	}
}

func TestRetrySQLiteBusyValueDoesNotRetryPermanentFailure(t *testing.T) {
	attempts := 0
	wantErr := errors.New("asset name conflict")
	_, err := retrySQLiteBusyValue(context.Background(), func() (string, error) {
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
