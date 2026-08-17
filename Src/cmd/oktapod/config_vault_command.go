package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/oktapod/oktapod/Src/internal/adapters/secretstore"
	"github.com/oktapod/oktapod/Src/internal/bootstrap"
	"golang.org/x/term"
)

func runConfig(args []string) error {
	if len(args) == 0 || args[0] != "doctor" {
		return fmt.Errorf("config subcommand is required; use `config doctor [--json]`")
	}
	flags := flag.NewFlagSet("config doctor", flag.ContinueOnError)
	jsonOutput := flags.Bool("json", false, "print machine-readable JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	config, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	report := map[string]any{
		"status": "ok", "config_path": config.ConfigPath, "source": config.ConfigSource,
		"profile": config.Profile, "listen_address": config.ListenAddress, "data_root": config.DataRoot,
		"database_driver": config.DatabaseDriver, "run_concurrency": config.RunConcurrency,
		"restart_required": false,
	}
	if *jsonOutput {
		data, _ := json.MarshalIndent(report, "", "  ")
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	}
	fmt.Fprintf(os.Stdout, "配置状态: 正常\n配置文件: %s\n来源: %s\nProfile: %s\n监听地址: %s\n数据目录: %s\n数据库: %s\nWorker 并发: %d\n", config.ConfigPath, config.ConfigSource, config.Profile, config.ListenAddress, config.DataRoot, config.DatabaseDriver, config.RunConcurrency)
	return nil
}

func runVault(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("vault subcommand is required; use status, initialize, confirm, recover, or rotate-recovery")
	}
	config, err := bootstrap.LoadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, actor, organization, err := openModelConfigStore(ctx, config)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.Bootstrap(ctx, actor, organization); err != nil {
		return err
	}
	keys, err := secretstore.NewVaultKeyManager(ctx, store, organization.ID, config.DataRoot)
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		status := keys.Status(ctx)
		fmt.Fprintf(os.Stdout, "密钥库状态: %s\n算法: %s\nKey Version: %d\n恢复口令: %s\nSecret 数量: %d\n说明: %s\n", status.State, status.Algorithm, status.KeyVersion, configuredText(status.RecoveryConfigured), status.SecretCount, status.Detail)
		return nil
	case "initialize":
		flags := flag.NewFlagSet("vault initialize", flag.ContinueOnError)
		recoveryOutput := flags.String("recovery-output", "", "write the one-time recovery code to a protected file and leave the vault pending")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if !term.IsTerminal(int(os.Stdin.Fd())) && strings.TrimSpace(*recoveryOutput) == "" {
			return fmt.Errorf("vault initialize requires an interactive TTY or --recovery-output")
		}
		code, _, err := keys.Initialize(ctx)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*recoveryOutput) != "" {
			if err := secretstore.WriteProtectedFile(*recoveryOutput, []byte(code+"\n")); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "恢复口令已写入受保护文件：%s\n密钥库状态: pending\n请使用 `vault confirm --recovery-file <path>` 完成确认。\n", *recoveryOutput)
			return nil
		}
		fmt.Fprintf(os.Stdout, "一次性恢复口令（请立即保存）：\n%s\n", code)
		confirmation, err := readRecoverySecret(os.Stdin, os.Stderr, "重新输入恢复口令以确认: ", "")
		if err != nil {
			return err
		}
		status, err := keys.Confirm(ctx, confirmation)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "密钥库状态: %s\n", status.State)
		return nil
	case "confirm":
		flags := flag.NewFlagSet("vault confirm", flag.ContinueOnError)
		file := flags.String("recovery-file", "", "read the recovery code from a protected file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		code, err := readRecoverySecret(os.Stdin, os.Stderr, "重新输入恢复口令以确认: ", *file)
		if err != nil {
			return err
		}
		status, err := keys.Confirm(ctx, code)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "密钥库状态: %s\n", status.State)
		return nil
	case "recover":
		flags := flag.NewFlagSet("vault recover", flag.ContinueOnError)
		file := flags.String("recovery-file", "", "read the recovery code from a protected file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		code, err := readRecoverySecret(os.Stdin, os.Stderr, "恢复口令（输入不会回显）: ", *file)
		if err != nil {
			return err
		}
		status, err := keys.Recover(ctx, code)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "密钥库状态: %s\n", status.State)
		return nil
	case "rotate-recovery":
		flags := flag.NewFlagSet("vault rotate-recovery", flag.ContinueOnError)
		recoveryOutput := flags.String("recovery-output", "", "write the new recovery code to a protected file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		code, status, err := keys.RotateRecovery(ctx)
		if err != nil {
			return err
		}
		if strings.TrimSpace(*recoveryOutput) != "" {
			if err := secretstore.WriteProtectedFile(*recoveryOutput, []byte(code+"\n")); err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "新的恢复口令已写入受保护文件：%s\n密钥库状态: %s\n", *recoveryOutput, status.State)
			return nil
		}
		fmt.Fprintf(os.Stdout, "新的恢复口令（旧口令已失效）：\n%s\n密钥库状态: %s\n", code, status.State)
		return nil
	default:
		return fmt.Errorf("unsupported vault subcommand %q; use status, initialize, confirm, recover, or rotate-recovery", args[0])
	}
}

func readRecoverySecret(input *os.File, prompt io.Writer, label, file string) (string, error) {
	var value string
	if strings.TrimSpace(file) != "" {
		data, err := os.ReadFile(file)
		if err != nil {
			return "", err
		}
		if len(data) > 1024 {
			return "", fmt.Errorf("recovery file is too large")
		}
		value = string(data)
	} else if term.IsTerminal(int(input.Fd())) {
		fmt.Fprint(prompt, label)
		data, err := term.ReadPassword(int(input.Fd()))
		fmt.Fprintln(prompt)
		if err != nil {
			return "", err
		}
		value = string(data)
	} else {
		data, err := io.ReadAll(bufio.NewReader(io.LimitReader(input, 1025)))
		if err != nil {
			return "", err
		}
		if len(data) > 1024 {
			return "", fmt.Errorf("recovery code exceeds 1024 bytes")
		}
		value = string(data)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("recovery code is required")
	}
	return value, nil
}

func configuredText(value bool) string {
	if value {
		return "已配置"
	}
	return "未配置"
}
