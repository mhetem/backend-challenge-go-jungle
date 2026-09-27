//go:build failpoints

package failpoint

import (
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		spec string
		want map[string]bool
		err  string
	}{
		{"", map[string]bool{}, ""},
		{"consumer.after_commit=exit", map[string]bool{ConsumerAfterCommit: true}, ""},
		{" outbox.after_claim=exit , outbox.after_publish=exit ", map[string]bool{OutboxAfterClaim: true, OutboxAfterPublish: true}, ""},
		{"consumer.after_comit=exit", nil, `FAILPOINTS: unknown failpoint "consumer.after_comit" (known: consumer.after_commit, outbox.after_claim, outbox.after_publish, resolver.after_reschedule, usecase.after_pending_reference_commit)`},
		{"consumer.after_commit", nil, `FAILPOINTS: consumer.after_commit: unknown action "" (known: exit)`},
		{"consumer.after_commit=panic", nil, `FAILPOINTS: consumer.after_commit: unknown action "panic" (known: exit)`},
		{"consumer.after_commit=exit,", nil, `FAILPOINTS: unknown failpoint "" (known: consumer.after_commit, outbox.after_claim, outbox.after_publish, resolver.after_reschedule, usecase.after_pending_reference_commit)`},
	}
	for _, tt := range tests {
		got, err := parse(tt.spec)
		if tt.err != "" {
			if err == nil || err.Error() != tt.err {
				t.Errorf("parse(%q) error = %v; want %s", tt.spec, err, tt.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parse(%q) = %v, %v; want %v", tt.spec, got, err, tt.want)
		}
	}
}

func TestHitIgnoresPointsThatAreNotArmed(t *testing.T) {
	armed = map[string]bool{OutboxAfterClaim: true}
	t.Cleanup(func() { armed = map[string]bool{} })
	Hit(ConsumerAfterCommit)
	Hit(ResolverAfterReschedule)
}
