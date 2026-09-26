package dbtest

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/mhetem/backend-challenge-go-jungle/migrations"
)

type Database struct {
	Name        string
	AppURL      string
	MigratorURL string
	App         *pgxpool.Pool
}

func New(t *testing.T) *Database {
	t.Helper()
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, Env(t, "ADMIN_DATABASE_URL"))
	if err != nil {
		t.Fatalf("connect as admin: %v", err)
	}
	name := "test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	ident := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+ident+" OWNER wallet_migrator"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(ctx, "DROP DATABASE "+ident+" WITH (FORCE)"); err != nil {
			t.Errorf("drop database %s: %v", name, err)
		}
		_ = admin.Close(ctx)
	})
	d := &Database{
		Name:        name,
		AppURL:      withDatabase(t, Env(t, "DATABASE_URL"), name),
		MigratorURL: withDatabase(t, Env(t, "MIGRATE_DATABASE_URL"), name),
	}
	if _, err := d.Migrations(t).Up(ctx); err != nil {
		t.Fatalf("migrate up: %v", err)
	}
	pool, err := pgxpool.New(ctx, d.AppURL)
	if err != nil {
		t.Fatalf("connect as app: %v", err)
	}
	t.Cleanup(pool.Close)
	d.App = pool
	return d
}

func (d *Database) Migrations(t *testing.T) *goose.Provider {
	t.Helper()
	cfg, err := pgx.ParseConfig(d.MigratorURL)
	if err != nil {
		t.Fatalf("parse migrator URL: %v", err)
	}
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = db.Close() })
	provider, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatalf("goose provider: %v", err)
	}
	return provider
}

func (d *Database) Migrator(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), d.MigratorURL)
	if err != nil {
		t.Fatalf("connect as migrator: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func Env(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	if value == "" {
		t.Fatalf("%s is not set: copy .env.example to .env and run through make, or export it", key)
	}
	return value
}

func withDatabase(t *testing.T, raw, name string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", raw, err)
	}
	u.Path = "/" + name
	return u.String()
}
