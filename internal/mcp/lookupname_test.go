package mcp

import (
	"reflect"
	"testing"
)

func TestParseLookupName(t *testing.T) {
	cases := []struct {
		in, hint string
		segs     []string
	}{
		{"Context.JSON", "", []string{"Context", "JSON"}},
		{"(*Context).JSON", "", []string{"Context", "JSON"}},
		{"gin.(*Context).JSON", "", []string{"gin", "Context", "JSON"}},
		{"Context.JSON(code int, obj any)", "", []string{"Context", "JSON"}},
		{"Context.JSON[T]", "", []string{"Context", "JSON"}},
		{"Client::get", "", []string{"Client", "get"}},
		{"::geo::Circle::area", "", []string{"geo", "Circle", "area"}},
		{"crate::net::connect", "", []string{"net", "connect"}},
		{"<MemStore as Store>::get", "", []string{"MemStore", "get"}},
		{"Cache::<T>::get", "", []string{"Cache", "get"}},
		{"Circle::area() const", "", []string{"Circle", "area"}},
		{"Constraint->matches", "", []string{"Constraint", "matches"}},
		{"Constraint::$version", "", []string{"Constraint", "version"}},
		{`\Composer\Semver\Constraint\Constraint::matches`, "", []string{"Composer", "Semver", "Constraint", "Constraint", "matches"}},
		{"StringUtils#isEmpty(CharSequence)", "", []string{"StringUtils", "isEmpty"}},
		{"ObjectMapper$DefaultTyping", "", []string{"ObjectMapper", "DefaultTyping"}},
		{"ObjectMapper.<init>", "", []string{"ObjectMapper", "ObjectMapper"}},
		{"Cart+Line.Render", "", []string{"Cart", "Line", "Render"}},
		{"Repo`1.Find", "", []string{"Repo", "Find"}},
		{"global::Acme.Shop.Cart.Add", "", []string{"Acme", "Shop", "Cart", "Add"}},
		{"Cart.Companion.empty", "", []string{"Cart", "empty"}},
		{"View.prototype.render", "", []string{"View", "render"}},
		{"Session.get(key:)", "", []string{"Session", "get"}},
		{"struct json_t", "", []string{"json_t"}},
		{"json_t->refcount", "", []string{"json_t", "refcount"}},
		{"$scope", "", []string{"$scope"}},
		{"value.c.json_object_get", "value.c", []string{"json_object_get"}},
		{"gin/render.JSON", "gin/render", []string{"JSON"}},
		{"render/json.go.JSON", "render/json.go", []string{"JSON"}},
		{"github.com/gin-gonic/gin.Context.JSON", "github.com/gin-gonic/gin", []string{"Context", "JSON"}},
		{"src/flask/helpers.py:url_for", "src/flask/helpers.py", []string{"url_for"}},
		{"src/flask/helpers.py::url_for", "src/flask/helpers.py", []string{"url_for"}},
		{"flask.helpers:url_for", "flask/helpers", []string{"url_for"}},
		{"./relation-id/RelationIdLoader", "relation-id/RelationIdLoader", []string{"RelationIdLoader"}},
		{"src/main/java/org/x/Streams.java", "src/main/java/org/x/Streams.java", []string{"Streams"}},
	}
	for _, c := range cases {
		q := parseLookupName(c.in)
		if q.pathHint != c.hint || !reflect.DeepEqual(q.segs, c.segs) {
			t.Errorf("%q: got hint=%q segs=%q, want hint=%q segs=%q", c.in, q.pathHint, q.segs, c.hint, c.segs)
		}
	}
}

func TestOSADistance(t *testing.T) {
	for _, c := range []struct {
		a, b string
		d    int
	}{{"Recovey", "Recovery", 1}, {"JOSN", "JSON", 1}, {"readValeu", "readValue", 1}, {"abc", "abc", 0}, {"kitten", "sitting", 3}} {
		if got := osaDistance(c.a, c.b); got != c.d {
			t.Errorf("osa(%q,%q)=%d want %d", c.a, c.b, got, c.d)
		}
	}
}
