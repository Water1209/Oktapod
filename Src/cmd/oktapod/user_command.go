package main

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/bootstrap"
	"github.com/oktapod/oktapod/Src/internal/domain/core"
)

// runUser is a local deployment entry, never exposed through HTTP.
func runUser(args []string) error {
	if len(args) == 0 || args[0] != "set-owner" {
		return fmt.Errorf("use user set-owner --username <name>")
	}
	flags := flag.NewFlagSet("user set-owner", flag.ContinueOnError)
	username := flags.String("username", "", "existing active login account")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*username) == "" || flags.NArg() != 0 {
		return fmt.Errorf("exactly one --username is required")
	}
	config, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	if config.Profile != core.ProfilePersonal {
		return fmt.Errorf("user set-owner supports the Personal profile only")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, actor, org, err := openModelConfigStore(ctx, config)
	if err != nil {
		return err
	}
	defer store.Close()
	local, ok := store.(interface {
		SetLocalOwner(context.Context, core.Scope, string) error
	})
	if !ok {
		return fmt.Errorf("local account recovery is unavailable")
	}
	return local.SetLocalOwner(ctx, core.Scope{OrganizationID: org.ID, UserID: actor.ID}, strings.TrimSpace(*username))
}
