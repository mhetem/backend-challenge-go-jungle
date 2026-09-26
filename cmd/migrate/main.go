package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/mhetem/backend-challenge-go-jungle/migrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := run(ctx, os.Args[1:])
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: migrate up|down|status|reset")
	}
	dsn := os.Getenv("MIGRATE_DATABASE_URL")
	if dsn == "" {
		return errors.New("MIGRATE_DATABASE_URL is not set")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return err
	}
	db := stdlib.OpenDB(*cfg)
	defer db.Close()
	provider, err := migrations.NewProvider(db)
	if err != nil {
		return err
	}
	switch args[0] {
	case "up":
		return report(provider.Up(ctx))
	case "down":
		result, err := provider.Down(ctx)
		if errors.Is(err, goose.ErrNoNextVersion) {
			fmt.Println("nothing to roll back")
			return nil
		}
		return report([]*goose.MigrationResult{result}, err)
	case "reset":
		return report(provider.DownTo(ctx, 0))
	case "status":
		return status(ctx, provider)
	}
	return fmt.Errorf("unknown command %q: want up, down, status or reset", args[0])
}

func report(results []*goose.MigrationResult, err error) error {
	for _, r := range results {
		if r != nil {
			fmt.Println(r)
		}
	}
	if len(results) == 0 && err == nil {
		fmt.Println("no migrations to run")
	}
	return err
}

func status(ctx context.Context, provider *goose.Provider) error {
	statuses, err := provider.Status(ctx)
	if err != nil {
		return err
	}
	for _, s := range statuses {
		applied := "-"
		if s.State == goose.StateApplied {
			applied = s.AppliedAt.UTC().Format(time.RFC3339)
		}
		fmt.Printf("%-8s %-20s %s\n", s.State, applied, s.Source.Path)
	}
	return nil
}
