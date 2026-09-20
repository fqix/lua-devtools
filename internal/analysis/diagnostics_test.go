package analysis

import "testing"

func TestUnclosedConstructDiagnostics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, source, expected, construct string
		line                              int
	}{
		{name: "function", source: "\nlocal function f()\nreturn 1", expected: "end", construct: "function", line: 2},
		{name: "if", source: "if true then\nprint(1)", expected: "end", construct: "if", line: 1},
		{name: "loop", source: "for i = 1, 2 do\nprint(i)", expected: "end", construct: "do", line: 1},
		{name: "repeat", source: "repeat\nprint(1)", expected: "until", construct: "repeat", line: 1},
		{name: "call", source: "print(1", expected: ")", construct: "(", line: 1},
		{name: "table", source: "local t = {x = 1", expected: "}", construct: "{", line: 1},
		{name: "ignore strings and comments", source: "local function f()\n-- end\nprint('end')", expected: "end", construct: "function", line: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := Parse([]byte(tt.source))
			defer f.Close()
			if len(f.Diagnostics) != 1 {
				t.Fatalf("diagnostics = %+v", f.Diagnostics)
			}
			d := f.Diagnostics[0]
			if d.Key != "diagnostic.unclosed" || d.Arg != tt.expected || d.Construct != tt.construct || d.OpeningLine != tt.line {
				t.Fatalf("diagnostic = %+v", d)
			}
		})
	}
}

func TestValidConstructsHaveNoDiagnostics(t *testing.T) {
	t.Parallel()
	f := Parse([]byte(`local function f()
  if true then
    for i = 1, 2 do print({i, "end"}) end
  elseif false then
    repeat local x = 1 until x > 0
  end
end`))
	defer f.Close()
	if len(f.Diagnostics) != 0 {
		t.Fatalf("diagnostics = %+v", f.Diagnostics)
	}
}
