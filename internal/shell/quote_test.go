package shell

import "testing"

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"/a/b":      "/a/b",
		"/a b":      "'/a b'",
		"it's":      `'it'\''s'`,
		"a\nb":      "'a\nb'",
		"tab\there": "'tab\there'",
		"":          "''",
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}
