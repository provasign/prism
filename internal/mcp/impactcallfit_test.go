package mcp

import "testing"

func TestCallFits(t *testing.T) {
	cases := []struct {
		sig, lang, call string
		want            bool
	}{
		{"def get_data(self, as_text=False):", "python", "r.get_data()", true},
		{"def get_data(self, as_text=False):", "python", "r.get_data(True)", true},
		{"def get_data(self, as_text=False):", "python", "r.get_data(as_text=True)", true},
		{"def get_data(self, as_text=False):", "python", "r.get_data(cache=False)", false},
		{"def get_data(self, as_text=False):", "python", "r.get_data(True, False)", false},
		{"def f(a, *, key):", "python", "f(1, key=2)", true},
		{"def f(a, *, key):", "python", "f(1, 2)", false},
		{"def f(a, **kw):", "python", "f(1, anything=2)", true},
		{"def f(*args):", "python", "f(1, 2, 3)", true},
		{"def f(a, b):", "python", "f(*xs)", true},
		{"def f(a, b):", "python", "f(1, x == 2)", true},
		{"match(method: string, path: string): Result<T>", "typescript", "path.match(/[^}]+/g)", false},
		{"match(method: string, path: string): Result<T>", "typescript", "r.match('GET', p)", true},
		{"get(key: Map<string, number>, fallback?: number)", "typescript", "m.get(k)", true},
		{"get(key: Map<string, number>, fallback?: number)", "typescript", "m.get(k, 1, 2)", false},
		{"use(...handlers: H[])", "typescript", "app.use(a, b, c)", true},
		{"route(path: string, app?: Hono)", "typescript", "app.route(a < b, c)", true},
		{"public function get(string $key, $default = null)", "php", "$c->get('k')", true},
		{"public function get(string $key, $default = null)", "php", "$c->get('k', 1)", true},
		{"public function get(string $key, $default = null)", "php", "$c->get(key: 'k', default: 2)", true},
		{"public function get(string $key, $default = null)", "php", "$c->get('k', 1, 2)", false},
		{"public function get(string $key, $default = null)", "php", "$c->get(name: 'x')", false},
		{"public function get(string $key, $default = null)", "php", "$c->get($a ? $b : $d)", true},
		{"public function get(?string $key)", "php", "$c->get()", false},
		{"public function get(string ...$keys)", "php", "$c->get('a', 'b', 'c')", true},
		{"public function get(string $key, $default = null)", "php", "$c->get(...$args)", true},
	}
	for _, c := range cases {
		sig, ok := parseFitSignature(c.sig, c.lang)
		if !ok {
			t.Fatalf("%q: signature not read", c.sig)
		}
		name := "get_data"
		for _, n := range []string{"get_data", "match", "get", "use", "route", "fetch", "f"} {
			if containsCall(c.call, n) {
				name = n
				break
			}
		}
		args, ok := callArgsAt(c.call, 1, 1, 0, name, c.lang)
		if !ok {
			t.Fatalf("%q: call not read", c.call)
		}
		if got := callFits(args, sig); got != c.want {
			t.Errorf("%s / %s: fits = %v, want %v (args %q)", c.sig, c.call, got, c.want, args)
		}
	}
}

func containsCall(call, name string) bool {
	for i := 0; i+len(name) < len(call); i++ {
		if call[i:i+len(name)] == name && call[i+len(name)] == '(' && (i == 0 || call[i-1] == '.' || call[i-1] == '>') {
			return true
		}
	}
	return false
}
