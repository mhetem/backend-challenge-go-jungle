package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"go.uber.org/fx"

	"github.com/mhetem/backend-challenge-go-jungle/internal/bootstrap"
	"github.com/mhetem/backend-challenge-go-jungle/internal/platform/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wallet:", err)
		os.Exit(1)
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		if err := healthcheck(cfg.AdminAddr); err != nil {
			fmt.Fprintln(os.Stderr, "wallet healthcheck:", err)
			os.Exit(1)
		}
		return
	}
	fx.New(bootstrap.Options(cfg)).Run()
}

func healthcheck(adminAddr string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + localAddr(adminAddr) + "/health/ready")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness returned %s", resp.Status)
	}
	return nil
}

func localAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	switch host {
	case "", "0.0.0.0", "::":
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
