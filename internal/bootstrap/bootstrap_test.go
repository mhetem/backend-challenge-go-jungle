package bootstrap

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/httpapi"
	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/oidc"
	"github.com/mhetem/backend-challenge-go-jungle/internal/adapters/sqs"
	"github.com/mhetem/backend-challenge-go-jungle/internal/app"
	"github.com/mhetem/backend-challenge-go-jungle/internal/auth"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/health"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/logging"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/metrics"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	vars := map[string]string{
		"INSTANCE_ID":  "bootstrap-test",
		"LOG_LEVEL":    "error",
		"DATABASE_URL": "postgres://wallet_app:wallet_app@127.0.0.1:1/wallet?sslmode=disable",
		"AWS_REGION":   "us-east-1",
	}
	cfg, err := config.Parse(func(key string) (string, bool) {
		v, ok := vars[key]
		return v, ok
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestGraphIsComplete(t *testing.T) {
	var (
		wallets *app.WalletService
		wagers  *app.WagerService
		runner  app.TxRunner
		pool    *pgxpool.Pool
		queues  *sqs.Client
		public  *httpapi.Server
		admin   *metrics.Admin
		checker *health.Checker
		tokens  *oidc.Verifier
		authn   auth.Authenticator
	)
	err := fx.ValidateApp(Options(testConfig(t)),
		fx.Populate(&wallets, &wagers, &runner, &pool, &queues, &public, &admin, &checker, &tokens, &authn))
	if err != nil {
		t.Fatal(err)
	}
}

func TestDrainIsTheFirstThingToStop(t *testing.T) {
	var sawDraining []bool
	probe := fx.Module("probe", fx.Invoke(func(lc fx.Lifecycle, checker *health.Checker) {
		lc.Append(fx.StopHook(func(context.Context) error {
			sawDraining = append(sawDraining, checker.Draining())
			return nil
		}))
	}))
	var checker *health.Checker
	service := fxtest.New(t,
		fx.Supply(testConfig(t)),
		logging.Module,
		health.Module,
		probe,
		fx.Invoke(announce),
		fx.Populate(&checker),
	)
	service.RequireStart()
	if checker.Draining() {
		t.Fatal("draining before stop")
	}
	service.RequireStop()
	if len(sawDraining) != 1 || !sawDraining[0] {
		t.Fatalf("module stop hook saw draining = %v; want true, because the root drain hook stops first", sawDraining)
	}
}
