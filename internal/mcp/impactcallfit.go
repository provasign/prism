package mcp

import (
	"regexp"
	"strings"
)

// A name-matched call is checked against the target's parameter list, read
// from the call's own source text. Without a compiler the name is all that
// links a call to the target, and a call the signature cannot accept is
// another method: hono's path.match(/re/) passes one argument where
// Router.match takes two, and werkzeug's get_data(cache=False) names a
// parameter Response.get_data does not have (it is Request.get_data).
// Defaults and keyword arguments make a plain argument count wrong, so the
// check is done only where those rules are known.

type fitParam struct {
	name        string
	optional    bool // has a default, or `?` in TypeScript
	keywordOnly bool // Python, after a bare `*`
}

type fitSignature struct {
	lang     string
	params   []fitParam
	varPos   bool // *args / ...rest / ...$rest
	varKw    bool // **kwargs
	keywords bool // calls may pass arguments by name
}

// signatureFitLanguage reports languages whose parameter rules callFits knows.
func signatureFitLanguage(lang string) bool {
	switch strings.ToLower(lang) {
	case "python", "typescript", "tsx", "javascript", "php":
		return true
	}
	return false
}

var (
	phpParamName  = regexp.MustCompile(`&?\$(\w+)`)
	pyKeywordArg  = regexp.MustCompile(`^([A-Za-z_]\w*)\s*=[^=]`)
	phpKeywordArg = regexp.MustCompile(`^([A-Za-z_]\w*)\s*:[^:]`)
)

// parseFitSignature reads the parameter list of a declaration's signature.
// ok is false when the signature has no readable parameter list.
func parseFitSignature(signature, lang string) (fitSignature, bool) {
	lang = strings.ToLower(lang)
	inner, ok := firstParenGroup(signature, 0, false)
	if !ok {
		return fitSignature{}, false
	}
	sig := fitSignature{lang: lang, keywords: lang == "python" || lang == "php"}
	keywordOnly := false
	for i, raw := range splitTopLevel(inner, false, lang == "typescript" || lang == "tsx") {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		var param fitParam
		switch lang {
		case "python":
			switch {
			case p == "/":
				continue
			case p == "*":
				keywordOnly = true
				continue
			case strings.HasPrefix(p, "**"):
				sig.varKw = true
				continue
			case strings.HasPrefix(p, "*"):
				sig.varPos, keywordOnly = true, true
				continue
			}
			param = fitParam{name: strings.TrimSpace(strings.FieldsFunc(p, func(r rune) bool { return r == ':' || r == '=' })[0]),
				optional: topLevelIndex(p, '=') >= 0, keywordOnly: keywordOnly}
			if i == 0 && (param.name == "self" || param.name == "cls") {
				continue
			}
		case "php":
			m := phpParamName.FindStringSubmatch(p)
			if m == nil {
				return fitSignature{}, false
			}
			if strings.Contains(p, "...") {
				sig.varPos = true
				continue
			}
			param = fitParam{name: m[1], optional: topLevelIndex(p, '=') >= 0}
		default: // TypeScript / JavaScript
			if strings.HasPrefix(p, "...") {
				sig.varPos = true
				continue
			}
			name := p
			if j := strings.IndexAny(name, ":="); j >= 0 {
				name = name[:j]
			}
			name = strings.TrimSpace(name)
			param = fitParam{name: strings.TrimSuffix(name, "?"), optional: topLevelIndex(p, '=') >= 0 || strings.HasSuffix(name, "?")}
			if i == 0 && param.name == "this" {
				continue
			}
		}
		sig.params = append(sig.params, param)
	}
	return sig, true
}

// callFits reports whether a call's arguments could bind to sig. Spread
// arguments, and anything it cannot read, fit.
func callFits(args []string, sig fitSignature) bool {
	positional := 0
	given := map[string]bool{}
	for _, a := range args {
		a = strings.TrimSpace(a)
		if strings.HasPrefix(a, "*") || strings.HasPrefix(a, "...") {
			return true
		}
		var kw []string
		switch sig.lang {
		case "python":
			kw = pyKeywordArg.FindStringSubmatch(a)
		case "php":
			kw = phpKeywordArg.FindStringSubmatch(a)
		}
		if kw != nil {
			given[kw[1]] = true
			continue
		}
		positional++
	}
	byPosition := 0
	for _, p := range sig.params {
		if !p.keywordOnly {
			byPosition++
		}
	}
	if positional > byPosition && !sig.varPos {
		return false
	}
	known := map[string]bool{}
	for i, p := range sig.params {
		known[p.name] = true
		filled := (!p.keywordOnly && i < positional) || given[p.name]
		if !filled && !p.optional {
			return false
		}
	}
	for k := range given {
		if !known[k] && !sig.varKw {
			return false
		}
	}
	return true
}

// callArgsAt returns the arguments of the call to name at line:col of body,
// whose first line is start. ok is false when the call cannot be read.
func callArgsAt(body string, start, line, col int, name, lang string) ([]string, bool) {
	lines := strings.Split(body, "\n")
	idx := line - start
	if start <= 0 || idx < 0 || idx >= len(lines) {
		return nil, false
	}
	text := lines[idx]
	at := col - 1
	if col <= 0 || at+len(name) > len(text) || text[at:at+len(name)] != name {
		at = strings.Index(text, name+"(")
		if at < 0 || strings.Count(text, name+"(") != 1 {
			return nil, false
		}
	}
	offset := len(strings.Join(lines[:idx], "\n"))
	if idx > 0 {
		offset++
	}
	from := offset + at + len(name)
	for from < len(body) && (body[from] == ' ' || body[from] == '\t') {
		from++
	}
	if from >= len(body) || body[from] != '(' {
		return nil, false // a generic call (f<T>(x)) or a reference, not read
	}
	js := strings.Contains("typescript tsx javascript", strings.ToLower(lang))
	inner, ok := firstParenGroup(body, from, js)
	if !ok {
		return nil, false
	}
	var args []string
	for _, a := range splitTopLevel(inner, js, false) {
		if strings.TrimSpace(a) != "" {
			args = append(args, a)
		}
	}
	return args, true
}

// scanTopLevel calls visit for each byte of s from from on that is outside
// quotes (and, in JavaScript, regex literals), with the bracket depth before
// it. angle counts <...> as brackets (type parameters in signatures). visit
// returns false to stop.
func scanTopLevel(s string, from int, js, angle bool, visit func(i, depth int) bool) {
	depth := 0
	prev := byte('(') // last significant byte: a regex can follow an operator
	for i := from; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"' || c == '\'' || c == '`':
			for i++; i < len(s) && s[i] != c; i++ {
				if s[i] == '\\' {
					i++
				}
			}
			prev = c
			continue
		case js && c == '/' && strings.IndexByte("(,=:[!&|?{};+-*%<>~^", prev) >= 0:
			inClass := false
			for i++; i < len(s) && s[i] != '\n'; i++ {
				if s[i] == '\\' {
					i++
				} else if s[i] == '[' {
					inClass = true
				} else if s[i] == ']' {
					inClass = false
				} else if s[i] == '/' && !inClass {
					break
				}
			}
			prev = 'x'
			continue
		}
		if !visit(i, depth) {
			return
		}
		switch {
		case c == '(' || c == '[' || c == '{' || (angle && c == '<'):
			depth++
		case c == ')' || c == ']' || c == '}' || (angle && c == '>' && depth > 0):
			depth--
		}
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			prev = c
		}
	}
}

// firstParenGroup returns the text inside the first balanced (...) at or
// after from.
func firstParenGroup(s string, from int, js bool) (string, bool) {
	open := strings.IndexByte(s[from:], '(')
	if open < 0 {
		return "", false
	}
	open += from
	end := -1
	scanTopLevel(s, open, js, false, func(i, depth int) bool {
		if depth == 1 && (s[i] == ')' || s[i] == ']' || s[i] == '}') {
			end = i
			return false
		}
		return true
	})
	if end < 0 || s[end] != ')' {
		return "", false
	}
	return s[open+1 : end], true
}

// splitTopLevel splits on commas outside brackets and quotes.
func splitTopLevel(s string, js, angle bool) []string {
	var parts []string
	last := 0
	scanTopLevel(s, 0, js, angle, func(i, depth int) bool {
		if depth == 0 && s[i] == ',' {
			parts = append(parts, s[last:i])
			last = i + 1
		}
		return true
	})
	return append(parts, s[last:])
}
