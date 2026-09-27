package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Config struct {
	URL              string
	ApplicationName  string
	MaxConns         int32
	LockTimeout      time.Duration
	StatementTimeout time.Duration
	TxAttempts       int
	TxBackoff        time.Duration
}

func NewPool(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, err
	}
	pc.MaxConns = cfg.MaxConns
	pc.ConnConfig.RuntimeParams["application_name"] = cfg.ApplicationName
	pc.ConnConfig.Tracer = queryTracer{}
	return pgxpool.NewWithConfig(ctx, pc)
}
