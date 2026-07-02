package sh

import "testing"

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"plain/path-1": "plain/path-1",
		"":             "''",
		"has space":    "'has space'",
		"it's fine":    "'it'\\''s fine'",
	}
	for input, want := range cases {
		if got := Quote(input); got != want {
			t.Fatalf("Quote(%q) = %q, want %q", input, got, want)
		}
	}
}
