package sqlite

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

var localUsernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{2,63}$`)

const (
	passwordIterations = 120_000
	passwordSaltBytes  = 16
	passwordKeyBytes   = 32
	maxLoginFailures   = 5
	loginLockDuration  = 15 * time.Minute
)

// ConfigureLocalPassword initializes a local account password exactly once.
// It is intended for first-start setup; subsequent changes must go through a
// dedicated administrative workflow rather than silently replacing a secret.
func (s *Store) ConfigureLocalPassword(ctx context.Context, organizationID, userID, password string) error {
	if len([]rune(password)) < 8 || len([]rune(password)) > 256 {
		return fmt.Errorf("password must be 8..256 characters")
	}
	hash, err := hashPassword(password)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash = ?, username = CASE WHEN username = '' THEN 'admin' ELSE username END WHERE id = ? AND password_hash = '' AND EXISTS (SELECT 1 FROM memberships WHERE organization_id = ? AND user_id = users.id AND status = 'active')`, hash, userID, organizationID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) AuthenticateLocal(ctx context.Context, organizationID string, input core.LocalLoginInput) (core.AuthSession, error) {
	username := strings.TrimSpace(input.Username)
	if username == "" || strings.TrimSpace(input.Password) == "" {
		return core.AuthSession{}, core.ErrInvalidCredential
	}
	var userID, passwordHash, lockedUntil string
	query := `SELECT u.id, u.password_hash, u.locked_until FROM users u JOIN memberships m ON m.user_id = u.id AND m.organization_id = ? AND m.status = 'active' WHERE (lower(u.username) = lower(?) OR lower(u.email) = lower(?) OR u.id = ?) LIMIT 1`
	if err := s.db.QueryRowContext(ctx, query, organizationID, username, username, username).Scan(&userID, &passwordHash, &lockedUntil); err != nil {
		return core.AuthSession{}, core.ErrInvalidCredential
	}
	if lockedUntil != "" {
		if locked, err := parseTime(lockedUntil); err == nil && time.Now().Before(locked) {
			return core.AuthSession{}, core.ErrInvalidCredential
		}
	}
	if passwordHash == "" || !verifyPassword(input.Password, passwordHash) {
		_, _ = s.db.ExecContext(ctx, `UPDATE users SET failed_login_attempts = failed_login_attempts + 1, locked_until = CASE WHEN failed_login_attempts + 1 >= ? THEN ? ELSE locked_until END WHERE id = ?`, maxLoginFailures, formatTime(time.Now().Add(loginLockDuration)), userID)
		return core.AuthSession{}, core.ErrInvalidCredential
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE users SET failed_login_attempts = 0, locked_until = '' WHERE id = ?`, userID); err != nil {
		return core.AuthSession{}, err
	}
	return s.CreateAuthSession(ctx, organizationID, core.CreateAuthSessionInput{UserID: userID, ExpiresInSeconds: 8 * 60 * 60})
}

func (s *Store) RegisterLocalUser(ctx context.Context, organizationID string, input core.LocalRegisterInput) (core.AuthSession, error) {
	input.Username = strings.TrimSpace(input.Username)
	input.DisplayName = strings.TrimSpace(input.DisplayName)
	if !localUsernamePattern.MatchString(input.Username) {
		return core.AuthSession{}, &core.ValidationError{Field: "username", Message: "must be 3..64 characters using letters, numbers, dot, underscore, or hyphen"}
	}
	if input.DisplayName == "" || len([]rune(input.DisplayName)) > 80 {
		return core.AuthSession{}, &core.ValidationError{Field: "display_name", Message: "must be 1..80 characters"}
	}
	if len([]rune(input.Password)) < 8 || len([]rune(input.Password)) > 256 {
		return core.AuthSession{}, &core.ValidationError{Field: "password", Message: "must be 8..256 characters"}
	}
	passwordHash, err := hashPassword(input.Password)
	if err != nil {
		return core.AuthSession{}, err
	}
	userID, err := newID()
	if err != nil {
		return core.AuthSession{}, err
	}
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	now := formatTime(time.Now())
	var registerErr error
	for attempt := 0; attempt < 5; attempt++ {
		registerErr = s.registerLocalUserTx(ctx, organizationID, userID, input, passwordHash, now)
		if registerErr == nil || !sqliteBusyError(registerErr) {
			break
		}
		if err := waitSQLiteRetry(ctx, attempt); err != nil {
			return core.AuthSession{}, err
		}
	}
	if registerErr != nil {
		return core.AuthSession{}, registerErr
	}
	var session core.AuthSession
	for attempt := 0; attempt < 5; attempt++ {
		session, err = s.CreateAuthSession(ctx, organizationID, core.CreateAuthSessionInput{UserID: userID, ExpiresInSeconds: 8 * 60 * 60})
		if err == nil || !sqliteBusyError(err) {
			return session, err
		}
		if waitErr := waitSQLiteRetry(ctx, attempt); waitErr != nil {
			return core.AuthSession{}, waitErr
		}
	}
	return core.AuthSession{}, err
}

func (s *Store) registerLocalUserTx(ctx context.Context, organizationID, userID string, input core.LocalRegisterInput, passwordHash, now string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE lower(username) = lower(?)`, input.Username).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return core.ErrUsernameTaken
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO users (id, display_name, email, username, password_hash, failed_login_attempts, locked_until, created_at, updated_at) VALUES (?, ?, '', ?, ?, 0, '', ?, ?)`, userID, input.DisplayName, input.Username, passwordHash, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memberships (organization_id, user_id, role, status, created_at, updated_at) VALUES (?, ?, 'member', 'active', ?, ?)`, organizationID, userID, now, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_context_settings (organization_id, owner_user_id, auto_compact, trigger_tokens, retain_tokens, summary_max_tokens, updated_at) VALUES (?, ?, 1, 100000, 20000, 4096, ?)`, organizationID, userID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func sqliteBusyError(err error) bool {
	message := strings.ToUpper(err.Error())
	return strings.Contains(message, "SQLITE_BUSY") || strings.Contains(message, "DATABASE IS LOCKED")
}

func waitSQLiteRetry(ctx context.Context, attempt int) error {
	timer := time.NewTimer(time.Duration(10*(1<<attempt)) * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func hashPassword(password string) (string, error) {
	salt := make([]byte, passwordSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := pbkdf2SHA256([]byte(password), salt, passwordIterations, passwordKeyBytes)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", passwordIterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return false
	}
	var iterations int
	if _, err := fmt.Sscanf(parts[1], "%d", &iterations); err != nil || iterations < 50_000 || iterations > 1_000_000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 8 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) != passwordKeyBytes {
		return false
	}
	got := pbkdf2SHA256([]byte(password), salt, iterations, len(want))
	return hmac.Equal(got, want)
}

func pbkdf2SHA256(password, salt []byte, iterations, keyLen int) []byte {
	result := make([]byte, 0, keyLen)
	for block := 1; len(result) < keyLen; block++ {
		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write([]byte{byte(block >> 24), byte(block >> 16), byte(block >> 8), byte(block)})
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for i := 1; i < iterations; i++ {
			mac = hmac.New(sha256.New, password)
			mac.Write(u)
			u = mac.Sum(nil)
			for j := range t {
				t[j] ^= u[j]
			}
		}
		result = append(result, t...)
	}
	return result[:keyLen]
}
