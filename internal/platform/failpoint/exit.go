//go:build failpoints

package failpoint

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

var known = []string{ConsumerAfterCommit, OutboxAfterClaim, OutboxAfterPublish, ResolverAfterReschedule, UsecaseAfterPendingReferenceCommit}

var armed = mustParse(os.Getenv("FAILPOINTS"))

func Hit(name string) {
	if armed[name] {
		fmt.Fprintf(os.Stderr, "failpoint %s: exiting with %d\n", name, ExitCode)
		os.Exit(ExitCode)
	}
}

func mustParse(spec string) map[string]bool {
	points, err := parse(spec)
	if err != nil {
		panic(err)
	}
	return points
}

func parse(spec string) (map[string]bool, error) {
	points := map[string]bool{}
	if strings.TrimSpace(spec) == "" {
		return points, nil
	}
	for part := range strings.SplitSeq(spec, ",") {
		name, action, _ := strings.Cut(strings.TrimSpace(part), "=")
		switch {
		case !slices.Contains(known, name):
			return nil, fmt.Errorf("FAILPOINTS: unknown failpoint %q (known: %s)", name, strings.Join(known, ", "))
		case action != "exit":
			return nil, fmt.Errorf("FAILPOINTS: %s: unknown action %q (known: exit)", name, action)
		}
		points[name] = true
	}
	return points, nil
}
