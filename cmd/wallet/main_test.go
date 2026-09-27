package main

import "testing"

func TestLocalAddr(t *testing.T) {
	tests := map[string]string{
		":9090":          "127.0.0.1:9090",
		"0.0.0.0:9090":   "127.0.0.1:9090",
		"[::]:9090":      "127.0.0.1:9090",
		"127.0.0.1:9091": "127.0.0.1:9091",
		"admin:9090":     "admin:9090",
		"[::1]:9090":     "[::1]:9090",
	}
	for in, want := range tests {
		if got := localAddr(in); got != want {
			t.Fatalf("localAddr(%q) = %q; want %q", in, got, want)
		}
	}
}
