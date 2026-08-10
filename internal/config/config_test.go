package config

import "testing"

// setRequired fills in everything Load insists on, so each test can vary the
// one variable it is about.
func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("FK_SIGN_SECRET", "0123456789abcdef0123456789abcdef")
	t.Setenv("FK_S3_ACCESS_KEY", "GKtest")
	t.Setenv("FK_S3_SECRET_KEY", "secret")
}

func TestTokenScopes(t *testing.T) {
	setRequired(t)
	t.Setenv("FK_TOKENS", "billing:tok_a, gallery-app:tok_b:upload ,archive:tok_c:full")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Token{
		"tok_a": {Service: "billing", Scope: "full"}, // omitted scope defaults to full
		"tok_b": {Service: "gallery-app", Scope: "upload"},
		"tok_c": {Service: "archive", Scope: "full"},
	}
	if len(cfg.Tokens) != len(want) {
		t.Fatalf("parsed %d tokens, want %d: %+v", len(cfg.Tokens), len(want), cfg.Tokens)
	}
	for token, w := range want {
		if got := cfg.Tokens[token]; got != w {
			t.Errorf("%s: got %+v, want %+v", token, got, w)
		}
	}
}

func TestTokenParseErrors(t *testing.T) {
	for name, tokens := range map[string]string{
		"unknown scope":  "svc:tok_a:admin",
		"missing token":  "svc:",
		"no colon":       "svc",
		"too many parts": "svc:tok_a:upload:extra",
		"empty service":  ":tok_a",
		"none at all":    "",
	} {
		t.Run(name, func(t *testing.T) {
			setRequired(t)
			t.Setenv("FK_TOKENS", tokens)
			if _, err := Load(); err == nil {
				t.Errorf("FK_TOKENS=%q: expected an error, got none", tokens)
			}
		})
	}
}
