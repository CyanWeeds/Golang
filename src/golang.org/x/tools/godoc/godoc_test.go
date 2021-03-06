// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package godoc

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestPkgLinkFunc(t *testing.T) {
	for _, tc := range []struct {
		path string
		want string
	}{
		{"/src/fmt", "pkg/fmt"},
		{"src/fmt", "pkg/fmt"},
		{"/fmt", "pkg/fmt"},
		{"fmt", "pkg/fmt"},
	} {
		if got := pkgLinkFunc(tc.path); got != tc.want {
			t.Errorf("pkgLinkFunc(%v) = %v; want %v", tc.path, got, tc.want)
		}
	}
}

func TestSrcPosLinkFunc(t *testing.T) {
	for _, tc := range []struct {
		src  string
		line int
		low  int
		high int
		want string
	}{
		{"/src/fmt/print.go", 42, 30, 50, "/src/fmt/print.go?s=30:50#L32"},
		{"/src/fmt/print.go", 2, 1, 5, "/src/fmt/print.go?s=1:5#L1"},
		{"/src/fmt/print.go", 2, 0, 0, "/src/fmt/print.go#L2"},
		{"/src/fmt/print.go", 0, 0, 0, "/src/fmt/print.go"},
		{"/src/fmt/print.go", 0, 1, 5, "/src/fmt/print.go?s=1:5#L1"},
		{"fmt/print.go", 0, 0, 0, "/src/fmt/print.go"},
		{"fmt/print.go", 0, 1, 5, "/src/fmt/print.go?s=1:5#L1"},
	} {
		if got := srcPosLinkFunc(tc.src, tc.line, tc.low, tc.high); got != tc.want {
			t.Errorf("srcLinkFunc(%v, %v, %v, %v) = %v; want %v", tc.src, tc.line, tc.low, tc.high, got, tc.want)
		}
	}
}

func TestSrcLinkFunc(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string
	}{
		{"/src/fmt/print.go", "/src/fmt/print.go"},
		{"src/fmt/print.go", "/src/fmt/print.go"},
		{"/fmt/print.go", "/src/fmt/print.go"},
		{"fmt/print.go", "/src/fmt/print.go"},
	} {
		if got := srcLinkFunc(tc.src); got != tc.want {
			t.Errorf("srcLinkFunc(%v) = %v; want %v", tc.src, got, tc.want)
		}
	}
}

func TestQueryLinkFunc(t *testing.T) {
	for _, tc := range []struct {
		src   string
		query string
		line  int
		want  string
	}{
		{"/src/fmt/print.go", "Sprintf", 33, "/src/fmt/print.go?h=Sprintf#L33"},
		{"/src/fmt/print.go", "Sprintf", 0, "/src/fmt/print.go?h=Sprintf"},
		{"src/fmt/print.go", "EOF", 33, "/src/fmt/print.go?h=EOF#L33"},
		{"src/fmt/print.go", "a%3f+%26b", 1, "/src/fmt/print.go?h=a%3f+%26b#L1"},
	} {
		if got := queryLinkFunc(tc.src, tc.query, tc.line); got != tc.want {
			t.Errorf("queryLinkFunc(%v, %v, %v) = %v; want %v", tc.src, tc.query, tc.line, got, tc.want)
		}
	}
}

func TestDocLinkFunc(t *testing.T) {
	for _, tc := range []struct {
		src   string
		ident string
		want  string
	}{
		{"fmt", "Sprintf", "/pkg/fmt/#Sprintf"},
		{"fmt", "EOF", "/pkg/fmt/#EOF"},
	} {
		if got := docLinkFunc(tc.src, tc.ident); got != tc.want {
			t.Errorf("docLinkFunc(%v, %v) = %v; want %v", tc.src, tc.ident, got, tc.want)
		}
	}
}

func TestSanitizeFunc(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string
	}{
		{},
		{"foo", "foo"},
		{"func   f()", "func f()"},
		{"func f(a int,)", "func f(a int)"},
		{"func f(a int,\n)", "func f(a int)"},
		{"func f(\n\ta int,\n\tb int,\n\tc int,\n)", "func f(a int, b int, c int)"},
		{"  (   a,   b,  c  )  ", "(a, b, c)"},
		{"(  a,  b, c    int, foo   bar  ,  )", "(a, b, c int, foo bar)"},
		{"{   a,   b}", "{a, b}"},
		{"[   a,   b]", "[a, b]"},
	} {
		if got := sanitizeFunc(tc.src); got != tc.want {
			t.Errorf("sanitizeFunc(%v) = %v; want %v", tc.src, got, tc.want)
		}
	}
}

// Test that we add <span id="StructName.FieldName"> elements
// to the HTML of struct fields.
func TestStructFieldsIDAttributes(t *testing.T) {
	got := linkifyStructFields(t, []byte(`
package foo

type T struct {
	NoDoc string

	// Doc has a comment.
	Doc string

	// Opt, if non-nil, is an option.
	Opt *int

	// ?????????? - ???????????? ????????.
	?????????? bool
}
`))
	want := `type T struct {
<span id="T.NoDoc"></span>NoDoc <a href="/pkg/builtin/#string">string</a>

<span id="T.Doc"></span><span class="comment">// Doc has a comment.</span>
Doc <a href="/pkg/builtin/#string">string</a>

<span id="T.Opt"></span><span class="comment">// Opt, if non-nil, is an option.</span>
Opt *<a href="/pkg/builtin/#int">int</a>

<span id="T.??????????"></span><span class="comment">// ?????????? - ???????????? ????????.</span>
?????????? <a href="/pkg/builtin/#bool">bool</a>
}`
	if got != want {
		t.Errorf("got: %s\n\nwant: %s\n", got, want)
	}
}

func linkifyStructFields(t *testing.T, src []byte) string {
	p := &Presentation{
		DeclLinks: true,
	}
	fset := token.NewFileSet()
	af, err := parser.ParseFile(fset, "foo.go", src, parser.ParseComments)
	if err != nil {
		t.Fatal(err)
	}
	genDecl := af.Decls[0].(*ast.GenDecl)
	pi := &PageInfo{
		FSet: fset,
	}
	return p.node_htmlFunc(pi, genDecl, true)
}

// Verify that scanIdentifier isn't quadratic.
// This doesn't actually measure and fail on its own, but it was previously
// very obvious when running by hand.
//
// TODO: if there's a reliable and non-flaky way to test this, do so.
// Maybe count user CPU time instead of wall time? But that's not easy
// to do portably in Go.
func TestStructField(t *testing.T) {
	for _, n := range []int{10, 100, 1000, 10000} {
		n := n
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			var buf bytes.Buffer
			fmt.Fprintf(&buf, "package foo\n\ntype T struct {\n")
			for i := 0; i < n; i++ {
				fmt.Fprintf(&buf, "\t// Field%d is foo.\n\tField%d int\n\n", i, i)
			}
			fmt.Fprintf(&buf, "}\n")
			linkifyStructFields(t, buf.Bytes())
		})
	}
}

func TestScanIdentifier(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"foo bar", "foo"},
		{"foo/bar", "foo"},
		{" foo", ""},
		{"??????", "??????"},
		{"f123", "f123"},
		{"123f", ""},
	}
	for _, tt := range tests {
		got := scanIdentifier([]byte(tt.in))
		if string(got) != tt.want {
			t.Errorf("scanIdentifier(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}
