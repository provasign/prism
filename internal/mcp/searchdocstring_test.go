package mcp

import (
	"strings"
	"testing"
)

func TestPythonDocstringLinesMarksDocstringsNotStringValues(t *testing.T) {
	src := strings.Split(`def make_response(*args):
    """Sometimes it is necessary to set additional headers.

        response = make_response(render_template('index.html'))
    """
    return current_app.make_response(args)

SCRIPT = """
complete = make_response(x)
"""

def one():
    r"""One-line docstring."""
    return 1`, "\n")
	marks := pythonDocstringLines(src)
	want := map[int]bool{1: true, 2: true, 3: true, 4: true, 12: true}
	for i := range src {
		if marks[i] != want[i] {
			t.Errorf("line %d %q: docstring=%v, want %v", i+1, src[i], marks[i], want[i])
		}
	}
}
