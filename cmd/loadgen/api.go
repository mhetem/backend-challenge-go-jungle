package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	providerClient = "provider-a"
	operatorClient = "wallet-backoffice"
	initialBalance = "1000000.00"
)

type api struct {
	http   *http.Client
	tokens *tokens
}

func newAPI(issuer string, concurrency int) *api {
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        4 * concurrency,
			MaxIdleConnsPerHost: concurrency,
			IdleConnTimeout:     90 * time.Second,
		},
	}
	return &api{http: client, tokens: &tokens{http: client, issuer: issuer, cached: map[string]token{}}}
}

type token struct {
	value   string
	expires time.Time
}

type tokens struct {
	http   *http.Client
	issuer string
	mu     sync.Mutex
	cached map[string]token
}

func (t *tokens) get(ctx context.Context, client string) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if cached, ok := t.cached[client]; ok && time.Until(cached.expires) > 30*time.Second {
		return cached.value, nil
	}
	variable := strings.ToUpper(strings.ReplaceAll(client, "-", "_")) + "_SECRET"
	secret := os.Getenv(variable)
	if secret == "" {
		return "", fmt.Errorf("%s is not set: run through make, or export .env", variable)
	}
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {client}, "client_secret": {secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.issuer+"/protocol/openid-connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("token for %s: %w", client, err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.AccessToken == "" {
		return "", fmt.Errorf("token for %s: %s", client, resp.Status)
	}
	t.cached[client] = token{value: body.AccessToken, expires: time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)}
	return body.AccessToken, nil
}

type reply struct {
	status     int
	replay     bool
	failure    string
	code       string
	id         string
	consistent bool
}

func (a *api) call(ctx context.Context, method, target, client, key string, body any) (reply, error) {
	bearer, err := a.tokens.get(ctx, client)
	if err != nil {
		return reply{}, err
	}
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return reply{}, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return reply{}, err
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return reply{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, err
	}
	var parsed struct {
		ID                 string `json:"id"`
		IdempotentReplay   bool   `json:"idempotentReplay"`
		FailureCode        string `json:"failureCode"`
		Code               string `json:"code"`
		Consistent         bool   `json:"consistent"`
		ContinuousVersions bool   `json:"continuousVersions"`
	}
	_ = json.Unmarshal(raw, &parsed)
	return reply{
		status:     resp.StatusCode,
		replay:     parsed.IdempotentReplay,
		failure:    parsed.FailureCode,
		code:       parsed.Code,
		id:         parsed.ID,
		consistent: parsed.Consistent && parsed.ContinuousVersions,
	}, nil
}

func (a *api) submit(ctx context.Context, target string, op operation) (reply, error) {
	return a.call(ctx, http.MethodPost, target+"/wagering/transactions", providerClient, providerClient+":"+op.extID, op.body())
}

type wallet struct {
	id     string
	player string
}

func openWallets(ctx context.Context, a *api, targets []string, n int) ([]wallet, error) {
	wallets := make([]wallet, n)
	for i := range wallets {
		player := uuid.NewString()
		r, err := a.call(ctx, http.MethodPost, targets[i%len(targets)]+"/wallets", operatorClient, "", map[string]any{
			"playerId":       player,
			"initialBalance": map[string]string{"amount": initialBalance, "currency": "BRL"},
		})
		switch {
		case err != nil:
			return nil, fmt.Errorf("opening wallet %d: %w", i+1, err)
		case r.status != http.StatusCreated || r.id == "":
			return nil, fmt.Errorf("opening wallet %d: HTTP %d", i+1, r.status)
		}
		wallets[i] = wallet{id: r.id, player: player}
	}
	return wallets, nil
}

func reconcileAll(ctx context.Context, a *api, targets []string, wallets []wallet) (int, error) {
	consistent := 0
	for i, w := range wallets {
		r, err := a.call(ctx, http.MethodPost, targets[i%len(targets)]+"/wallets/"+w.id+"/reconciliation", operatorClient, "", nil)
		if err != nil {
			return consistent, fmt.Errorf("reconciling wallet %s: %w", w.id, err)
		}
		if r.status == http.StatusOK && r.consistent {
			consistent++
		}
	}
	return consistent, nil
}

func (a *api) scrape(ctx context.Context, admin string) (map[string]float64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, admin+"/metrics", nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s/metrics: HTTP %d", admin, resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseMetrics(string(raw)), nil
}

func (a *api) scrapeAll(ctx context.Context, admins []string) ([]map[string]float64, error) {
	if len(admins) == 0 {
		return nil, errors.New("no admin URLs")
	}
	instances := make([]map[string]float64, 0, len(admins))
	for _, admin := range admins {
		m, err := a.scrape(ctx, admin)
		if err != nil {
			return nil, err
		}
		instances = append(instances, m)
	}
	return instances, nil
}

func parseMetrics(text string) map[string]float64 {
	series := map[string]float64{}
	for line := range strings.Lines(text) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		if i < 0 {
			continue
		}
		if v, err := strconv.ParseFloat(line[i+1:], 64); err == nil {
			series[line[:i]] = v
		}
	}
	return series
}

func total(instances []map[string]float64, series string) float64 {
	sum := 0.0
	for _, m := range instances {
		sum += m[series]
	}
	return sum
}

func peak(instances []map[string]float64, series string) float64 {
	top := 0.0
	for _, m := range instances {
		top = max(top, m[series])
	}
	return top
}
