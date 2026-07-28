package application

import (
	"context"
	"errors"
	"testing"
)

func TestWithPrincipalPublishesValidatedIdentityAndScope(t *testing.T) {
	principal := Principal{
		OrganizationID:       "org-1",
		UserID:               "user-1",
		AuthenticationMethod: AuthenticationSession,
		Administrator:        true,
	}
	ctx := WithPrincipal(context.Background(), principal)

	resolved, ok := PrincipalFromContext(ctx)
	if !ok || resolved != principal {
		t.Fatalf("principal=%#v ok=%v", resolved, ok)
	}
	scope, ok := ScopeFromContext(ctx)
	if !ok || scope.OrganizationID != principal.OrganizationID || scope.UserID != principal.UserID {
		t.Fatalf("scope=%#v ok=%v", scope, ok)
	}
	if !IsAdmin(ctx) {
		t.Fatal("administrator flag was not propagated")
	}
}

func TestLocalPrincipalResolverRejectsNonLocalCredentials(t *testing.T) {
	resolver := LocalPrincipalResolver{Principal: Principal{OrganizationID: "org-1", UserID: "user-1"}}
	if _, err := resolver.ResolvePrincipal(t.Context(), PrincipalCredential{Method: AuthenticationSession}); !errors.Is(err, ErrPrincipalRequired) {
		t.Fatalf("session credential err=%v", err)
	}
	principal, err := resolver.ResolvePrincipal(t.Context(), PrincipalCredential{Method: AuthenticationLocal})
	if err != nil || principal.AuthenticationMethod != AuthenticationLocal {
		t.Fatalf("principal=%#v err=%v", principal, err)
	}
}
