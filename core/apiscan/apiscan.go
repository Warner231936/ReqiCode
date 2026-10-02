// Package apiscan extracts a package's public API exactly, from the source, using
// the Go parser rather than a model.
//
// The motivation is a measured failure. Asked to write a test for
// regression.Project, a 14B coder model produced code referencing a hallucinated
// API across five attempts, with `undefined:` as the dominant failure kind. The
// model was not being careless; the prompt contained a function body and nothing
// that demonstrated how the surrounding package is actually used.
//
// Handing it a signature list parsed out of the AST is therefore not a
// convenience. It is the difference between asking the model to guess an API and
// showing it the API, and it is derived from the compiler's own view of the
// source rather than from another inference, which makes it the first genuinely
// independent channel in the verification story: nothing here was generated, so
// nothing here can be confidently wrong in the way a generated API summary can.
package apiscan

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Func is an exported function or method.
type Func struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
	// Receiver is empty for package-level functions.
	Receiver string  `json:"receiver,omitempty"`
	Params   []Param `json:"params"`
	Results  []Param `json:"results"`
	// Doc is the leading comment, which is where the contract usually lives.
	Doc  string `json:"doc,omitempty"`
	Line int    `json:"line"`
}

// Param is one parameter or result.
type Param struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Struct is an exported struct type and its fields.
type Struct struct {
	Name    string   `json:"name"`
	Fields  []Param  `json:"fields"`
	Methods []string `json:"methods,omitempty"`
	Doc     string   `json:"doc,omitempty"`
	Line    int      `json:"line"`
}

// Iface is an exported interface.
type Iface struct {
	Name    string   `json:"name"`
	Methods []string `json:"methods"`
	Doc     string   `json:"doc,omitempty"`
}

// Constant is an exported constant.
type Constant struct {
	Name  string `json:"name"`
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
	Doc   string `json:"doc,omitempty"`
}

// API is everything a test writer needs to know about a package.
type API struct {
	Package    string     `json:"package"`
	Functions  []Func     `json:"functions"`
	Structs    []Struct   `json:"structs"`
	Interfaces []Iface    `json:"interfaces"`
	Constants  []Constant `json:"constants"`
	// TypeNames lists exported type names, for disambiguation in messages.
	TypeNames []string `json:"type_names"`
}

// Scan parses a directory and returns its exported API.
//
// Returns an error rather than a partial API on parse failure. A partial API is
// worse than none here: a missing symbol looks identical to a symbol that does
// not exist, which is precisely the hallucination this package exists to prevent.
func Scan(dir string) (*API, error) {
	fset := token.NewFileSet()

	var api API
	api.Package = filepath.Base(filepath.Clean(dir))

	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		// Tests are excluded: their symbols are not part of the package's API and
		// including them invites a model to test a test.
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", dir, err)
	}

	for name, pkg := range pkgs {
		api.Package = name
		for _, file := range pkg.Files {
			scanFile(file, fset, &api)
		}
	}

	sortAPI(&api)
	return &api, nil
}

func scanFile(file *ast.File, fset *token.FileSet, api *API) {
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			f := buildFunc(d, fset)
			if d.Recv != nil && len(d.Recv.List) > 0 {
				recv := exprString(d.Recv.List[0].Type)
				f.Receiver = recv
				// Attach to the struct so a test writer sees the method with it.
				for i := range api.Structs {
					if api.Structs[i].Name == recv {
						api.Structs[i].Methods = append(api.Structs[i].Methods, f.Signature)
					}
				}
			}
			api.Functions = append(api.Functions, f)

		case *ast.GenDecl:
			switch d.Tok {
			case token.TYPE:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ts.Name.IsExported() {
						continue
					}
					doc := docText(d.Doc, ts.Doc)
					switch t := ts.Type.(type) {
					case *ast.StructType:
						s := Struct{
							Name: ts.Name.Name,
							Doc:  doc,
							Line: fset.Position(ts.Pos()).Line,
						}
						for _, f := range t.Fields.List {
							for _, n := range fieldNames(f) {
								s.Fields = append(s.Fields, Param{Name: n, Type: exprString(f.Type)})
							}
						}
						api.Structs = append(api.Structs, s)
					case *ast.InterfaceType:
						i := Iface{Name: ts.Name.Name, Doc: doc}
						for _, m := range t.Methods.List {
							for _, n := range fieldNames(m) {
								i.Methods = append(i.Methods, n+" "+exprString(m.Type))
							}
						}
						api.Interfaces = append(api.Interfaces, i)
					default:
						api.TypeNames = append(api.TypeNames, ts.Name.Name)
					}
				}
			case token.CONST:
				for _, spec := range d.Specs {
					vs, ok := spec.(*ast.ValueSpec)
					if !ok {
						continue
					}
					typ := ""
					if vs.Type != nil {
						typ = exprString(vs.Type)
					}
					val := ""
					if len(vs.Values) > 0 {
						val = exprString(vs.Values[0])
					}
					for _, n := range vs.Names {
						if !n.IsExported() {
							continue
						}
						api.Constants = append(api.Constants, Constant{
							Name:  n.Name,
							Type:  typ,
							Value: val,
							Doc:   docText(d.Doc, vs.Doc),
						})
					}
				}
			}
		}
	}
}

func buildFunc(d *ast.FuncDecl, fset *token.FileSet) Func {
	f := Func{
		Name: d.Name.Name,
		Doc:  docText(d.Doc, nil),
		Line: fset.Position(d.Pos()).Line,
	}

	var params []string
	if d.Type.Params != nil {
		for _, p := range d.Type.Params.List {
			typ := exprString(p.Type)
			names := fieldNames(p)
			if len(names) == 0 {
				f.Params = append(f.Params, Param{Type: typ})
				params = append(params, typ)
				continue
			}
			for _, n := range names {
				f.Params = append(f.Params, Param{Name: n, Type: typ})
				params = append(params, n+" "+typ)
			}
		}
	}

	var results []string
	if d.Type.Results != nil {
		for _, r := range d.Type.Results.List {
			typ := exprString(r.Type)
			names := fieldNames(r)
			if len(names) == 0 {
				f.Results = append(f.Results, Param{Type: typ})
				results = append(results, typ)
				continue
			}
			for _, n := range names {
				f.Results = append(f.Results, Param{Name: n, Type: typ})
				results = append(results, n+" "+typ)
			}
		}
	}

	f.Signature = "func " + f.Name + "(" + strings.Join(params, ", ") + ")"
	if len(results) > 0 {
		if len(results) == 1 && !strings.Contains(results[0], " ") {
			f.Signature += " " + results[0]
		} else {
			f.Signature += " (" + strings.Join(results, ", ") + ")"
		}
	}
	if f.Doc != "" {
		f.Signature = f.Doc + "\n" + f.Signature
	}
	return f
}

func fieldNames(f *ast.Field) []string {
	if len(f.Names) == 0 {
		return nil
	}
	out := make([]string, 0, len(f.Names))
	for _, n := range f.Names {
		out = append(out, n.Name)
	}
	return out
}

func docText(a, b *ast.CommentGroup) string {
	if a != nil {
		return strings.TrimSpace(a.Text())
	}
	if b != nil {
		return strings.TrimSpace(b.Text())
	}
	return ""
}

// exprString renders an AST expression back to source text.
//
// Rendering rather than pattern-matching is deliberate: the type is then exact by
// construction, including generics, embedded types and pointer receivers, none of
// which a hand-written matcher would reliably handle.
func exprString(e ast.Expr) string {
	switch t := e.(type) {
	case nil:
		return ""
	case *ast.Ident:
		return t.Name
	case *ast.BasicLit:
		return t.Value
	case *ast.SelectorExpr:
		return exprString(t.X) + "." + t.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(t.X)
	case *ast.ArrayType:
		if t.Len == nil {
			return "[]" + exprString(t.Elt)
		}
		return "[" + exprString(t.Len) + "]" + exprString(t.Elt)
	case *ast.MapType:
		return "map[" + exprString(t.Key) + "]" + exprString(t.Value)
	case *ast.Ellipsis:
		return "..." + exprString(t.Elt)
	case *ast.FuncType:
		return "func"
	case *ast.ChanType:
		switch {
		case t.Dir == ast.SEND:
			return "chan<- " + exprString(t.Value)
		case t.Dir == ast.RECV:
			return "<-chan " + exprString(t.Value)
		default:
			return "chan " + exprString(t.Value)
		}
	case *ast.InterfaceType:
		return "any"
	case *ast.ParenExpr:
		return "(" + exprString(t.X) + ")"
	case *ast.IndexExpr:
		// Generic instantiation: Foo[int].
		return exprString(t.X) + "[" + exprString(t.Index) + "]"
	case *ast.IndexListExpr:
		args := make([]string, 0, len(t.Indices))
		for _, idx := range t.Indices {
			args = append(args, exprString(idx))
		}
		return exprString(t.X) + "[" + strings.Join(args, ", ") + "]"
	case *ast.UnaryExpr:
		return t.Op.String() + exprString(t.X)
	case *ast.BinaryExpr:
		return exprString(t.X) + " " + t.Op.String() + " " + exprString(t.Y)
	case *ast.CompositeLit:
		elts := make([]string, 0, len(t.Elts))
		for _, el := range t.Elts {
			elts = append(elts, exprString(el))
		}
		return exprString(t.Type) + "{" + strings.Join(elts, ", ") + "}"
	case *ast.CallExpr:
		args := make([]string, 0, len(t.Args))
		for _, a := range t.Args {
			args = append(args, exprString(a))
		}
		return exprString(t.Fun) + "(" + strings.Join(args, ", ") + ")"
	case *ast.TypeAssertExpr:
		return exprString(t.X) + ".(" + exprString(t.Type) + ")"
	}
	// Unknown form: fall back to a marker rather than a wrong type. A wrong type
	// here would be hallucinated by proxy, which is the failure this package
	// exists to remove.
	return "?"
}

func sortAPI(api *API) {
	sort.Slice(api.Functions, func(i, j int) bool { return api.Functions[i].Name < api.Functions[j].Name })
	sort.Slice(api.Structs, func(i, j int) bool { return api.Structs[i].Name < api.Structs[j].Name })
	sort.Slice(api.Interfaces, func(i, j int) bool { return api.Interfaces[i].Name < api.Interfaces[j].Name })
	sort.Slice(api.Constants, func(i, j int) bool { return api.Constants[i].Name < api.Constants[j].Name })
	sort.Strings(api.TypeNames)

	for i := range api.Structs {
		sort.Strings(api.Structs[i].Methods)
	}
}

// Summary renders the API as a compact, prompt-ready reference.
//
// Sized by information rather than by line count: a signature with a doc comment
// is worth including whole, and one without is worth a line. Truncating the
// middle would drop exactly the doc comments that carry the contract.
func (a *API) Summary(limit int) string {
	if a == nil {
		return ""
	}

	var lines []string
	lines = append(lines, "package "+a.Package)

	for _, c := range a.Constants {
		line := "const " + c.Name
		if c.Type != "" {
			line += " " + c.Type
		}
		if c.Value != "" {
			line += " = " + c.Value
		}
		lines = append(lines, line)
	}

	for _, t := range a.TypeNames {
		lines = append(lines, "type "+t)
	}

	for _, s := range a.Structs {
		var b strings.Builder
		b.WriteString("type " + s.Name + " struct {")
		for _, f := range s.Fields {
			if f.Name != "" {
				b.WriteString("\n\t" + f.Name + " " + f.Type)
			} else {
				b.WriteString("\n\t" + f.Type)
			}
		}
		b.WriteString("\n}")
		for _, m := range s.Methods {
			b.WriteString("\nfunc (x *" + s.Name + ") " + m)
		}
		if s.Doc != "" {
			b.WriteString("\n// " + s.Doc)
		}
		lines = append(lines, b.String())
	}

	for _, i := range a.Interfaces {
		var b strings.Builder
		b.WriteString("type " + i.Name + " interface {")
		for _, m := range i.Methods {
			b.WriteString("\n\t" + m)
		}
		b.WriteString("\n}")
		lines = append(lines, b.String())
	}

	for _, f := range a.Functions {
		if f.Receiver != "" {
			continue // already shown with its type
		}
		lines = append(lines, f.Signature)
	}

	out := strings.Join(lines, "\n")
	if limit > 0 && len(out) > limit {
		// Keep the head, which carries the types and signatures a caller must
		// get right, and note the truncation rather than silently cutting.
		out = out[:limit] + "\n// ... truncated, request the rest"
	}
	return out
}

// Lookup finds a function by name.
func (a *API) Lookup(name string) (Func, bool) {
	for _, f := range a.Functions {
		if f.Name == name {
			return f, true
		}
	}
	return Func{}, false
}

// SignatureOf renders a call expression for a function, with zero values for
// parameters, which is what a smoke test needs to compile against.
func (a *API) SignatureOf(name string) (Func, bool) {
	return a.Lookup(name)
}

// ZeroValue returns a compilable zero literal for a type expression.
//
// Used by the mechanical test synthesiser. Returns ok=false for types with no
// obvious zero literal, which is the honest answer: a slice is nil, a struct is
// T{}, a pointer is nil, but a channel or a func is not constructible in a way
// worth putting in a test.
func ZeroValue(typeExpr string) (string, bool) {
	switch typeExpr {
	case "string":
		return `""`, true
	case "bool":
		return "false", true
	case "int", "int8", "int16", "int32", "int64",
		"uint", "uint8", "uint16", "uint32", "uint64", "rune", "byte":
		return "0", true
	case "float32", "float64", "complex64", "complex128":
		return "0", true
	case "error":
		return "nil", true
	case "any", "interface{}":
		return "nil", true
	case "time.Duration":
		return "0", true
	}
	switch {
	case strings.HasPrefix(typeExpr, "*"):
		return "nil", true
	case strings.HasPrefix(typeExpr, "[]"):
		return "nil", true
	case strings.HasPrefix(typeExpr, "map["):
		return "nil", true
	case strings.HasPrefix(typeExpr, "chan"), strings.HasPrefix(typeExpr, "<-chan"):
		return "nil", true
	case strings.HasPrefix(typeExpr, "func("), strings.HasPrefix(typeExpr, "interface"):
		return "nil", true
	case strings.HasPrefix(typeExpr, "struct{"):
		return typeExpr, true
	}
	// A bare identifier is either a named type (struct{} works) or an
	// unresolvable reference. Only the former is safe to guess at, and guessing
	// is exactly what this package exists to avoid.
	if isIdentifier(typeExpr) {
		return typeExpr + "{}", true
	}
	return "", false
}

func isIdentifier(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '_' {
			continue
		}
		if r >= '0' && r <= '9' && i > 0 {
			continue
		}
		return false
	}
	return true
}

// Quote renders a string safely for inclusion in generated source.
func Quote(s string) string {
	return strconv.Quote(s)
}
