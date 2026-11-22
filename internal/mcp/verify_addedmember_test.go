package mcp

import (
	"strings"
	"testing"
)

// BUG-03 (2026-11-22): a member added to a Go interface made verify say
// "automated verification not implemented for this kind". The types the
// compiler sees used as the interface and that lack the new member are
// missed sites; the one that implements it is not.
func TestVerifyGoAddedInterfaceMemberNamesMissingImplementations(t *testing.T) {
	base := "package prov\n\ntype Provider interface {\n\tName() string\n}\n\n" +
		"type ollama struct{}\n\nfunc (ollama) Name() string { return \"o\" }\n\n" +
		"type anthropic struct{}\n\nfunc (*anthropic) Name() string { return \"a\" }\n\n" +
		"func New(k string) Provider {\n\tif k == \"a\" {\n\t\treturn &anthropic{}\n\t}\n\treturn ollama{}\n}\n"
	edited := strings.Replace(base, "\tName() string\n}", "\tName() string\n\tClose() error\n}", 1)
	edited = strings.Replace(edited, "type anthropic struct{}", "func (ollama) Close() error { return nil }\n\ntype anthropic struct{}", 1)
	m := variadicVerify(t, map[string]string{"prov/prov.go": base}, map[string]string{"prov/prov.go": edited})
	if m["verdict"] != "incomplete" {
		t.Fatalf("verdict = %v, want incomplete (anthropic lacks Close)\n%v", m["verdict"], m)
	}
	var names []string
	for _, s := range anySlice(m["missedSites"]) {
		switch site := s.(type) {
		case missedSite:
			names = append(names, site.Symbol)
		case map[string]any:
			names = append(names, stringArg(site, "symbol", ""))
		}
	}
	if len(names) != 1 || names[0] != "anthropic" {
		t.Fatalf("missed sites = %v, want [anthropic]", names)
	}
	if u, _ := m["unverifiedSeeds"].([]string); len(u) > 0 {
		t.Fatalf("an added member with a computed answer must not stay unverified: %v", u)
	}
}

// All implementations updated: complete, not review.
func TestVerifyGoAddedInterfaceMemberAllImplemented(t *testing.T) {
	base := "package prov\n\ntype Provider interface {\n\tName() string\n}\n\n" +
		"type ollama struct{}\n\nfunc (ollama) Name() string { return \"o\" }\n\n" +
		"func New() Provider { return ollama{} }\n"
	edited := strings.Replace(base, "\tName() string\n}", "\tName() string\n\tClose() error\n}", 1)
	edited += "\nfunc (ollama) Close() error { return nil }\n"
	m := variadicVerify(t, map[string]string{"prov/prov.go": base}, map[string]string{"prov/prov.go": edited})
	if m["verdict"] != "complete" {
		t.Fatalf("verdict = %v, want complete\n%v", m["verdict"], m)
	}
}
