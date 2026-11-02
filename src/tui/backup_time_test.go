package main

import "testing"

func TestBrisbaneBackupTime(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{"2026-12-12T09:39:00Z", "7:39pm 12th Dec 2026"},
		{"2026-12-20T14:00:00Z", "12:00am 21st Dec 2026"},
		{"2026-01-01T00:00:00Z", "10:00am 1st Jan 2026"},
		{"", "Unknown"},
		{"invalid", "invalid"},
	} {
		if got := brisbaneBackupTime(tc.input); got != tc.want {
			t.Errorf("%q: got %q, want %q", tc.input, got, tc.want)
		}
	}
}
