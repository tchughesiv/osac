package main

import "testing"

func TestResourceState(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		resource map[string]any
		want     string
	}{
		{name: "status state", resource: map[string]any{"status": map[string]any{"state": "COMPUTE_INSTANCE_STATE_READY"}}, want: "COMPUTE_INSTANCE_STATE_READY"},
		{name: "missing status", resource: map[string]any{}, want: "unknown"},
		{name: "missing state", resource: map[string]any{"status": map[string]any{}}, want: "unknown"},
		{name: "invalid status", resource: map[string]any{"status": "ready"}, want: "unknown"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := resourceState(test.resource); got != test.want {
				t.Errorf("resourceState() = %q, want %q", got, test.want)
			}
		})
	}
}
