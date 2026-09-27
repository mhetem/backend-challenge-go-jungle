package httpapi

import (
	"encoding/json"
	"net/http"
)

type problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Code      string `json:"code"`
	Detail    string `json:"detail,omitempty"`
	Retryable bool   `json:"retryable"`
}

func writeProblem(w http.ResponseWriter, status int, code, detail string, retryable bool) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem{
		Type:      "about:blank",
		Title:     http.StatusText(status),
		Status:    status,
		Code:      code,
		Detail:    detail,
		Retryable: retryable,
	})
}
