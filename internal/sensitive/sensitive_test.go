package sensitive

import "testing"

func TestMatch(t *testing.T) {
	for p, want := range map[string]bool{
		".env": true, "config/.env.production": true, "deploy/server.pem": true, "keys/id_rsa": true,
		"keys/id_rsa.pub": true, "aws/credentials": true, "app/secrets.yaml": true, ".npmrc": true,
		".env.example": false, "main.go": false, "docs/env.md": false, "src/keyboard.ts": false,
	} {
		if got := Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
	Add("private/*.json")
	if !Match("private/token.json") || Match("public/token.json") {
		t.Error("added path pattern")
	}
}
