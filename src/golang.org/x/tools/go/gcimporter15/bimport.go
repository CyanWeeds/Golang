// Copyright 2015 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// This file is a copy of $GOROOT/src/go/internal/gcimporter/bimport.go.

package gcimporter

import (
	"encoding/binary"
	"fmt"
	"go/constant"
	"go/token"
	"go/types"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

type importer struct {
	imports map[string]*types.Package
	data    []byte
	path    string
	buf     []byte // for reading strings
	version int    // export format version

	// object lists
	strList       []string         // in order of appearance
	pkgList       []*types.Package // in order of appearance
	typList       []types.Type     // in order of appearance
	trackAllTypes bool

	// position encoding
	posInfoFormat bool
	prevFile      string
	prevLine      int
	fset          *token.FileSet
	files         map[string]*token.File

	// debugging support
	debugFormat bool
	read        int // bytes read
}

// BImportData imports a package from the serialized package data
// and returns the number of bytes consumed and a reference to the package.
// If the export data version is not recognized or the format is otherwise
// compromised, an error is returned.
func BImportData(fset *token.FileSet, imports map[string]*types.Package, data []byte, path string) (_ int, _ *types.Package, err error) {
	// catch panics and return them as errors
	defer func() {
		if e := recover(); e != nil {
			// The package (filename) causing the problem is added to this
			// error by a wrapper in the caller (Import in gcimporter.go).
			err = fmt.Errorf("cannot import, possibly version skew (%v) - reinstall package", e)
		}
	}()

	p := importer{
		imports: imports,
		data:    data,
		path:    path,
		version: -1,           // unknown version
		strList: []string{""}, // empty string is mapped to 0
		fset:    fset,
		files:   make(map[string]*token.File),
	}

	// read version info
	var versionstr string
	if b := p.rawByte(); b == 'c' || b == 'd' {
		// Go1.7 encoding; first byte encodes low-level
		// encoding format (compact vs debug).
		// For backward-compatibility only (avoid problems with
		// old installed packages). Newly compiled packages use
		// the extensible format string.
		// TODO(gri) Remove this support eventually; after Go1.8.
		if b == 'd' {
			p.debugFormat = true
		}
		p.trackAllTypes = p.rawByte() == 'a'
		p.posInfoFormat = p.int() != 0
		versionstr = p.string()
		if versionstr == "v1" {
			p.version = 0
		}
	} else {
		// Go1.8 extensible encoding
		// read version string and extract version number (ignore anything after the version number)
		versionstr = p.rawStringln(b)
		if s := strings.SplitN(versionstr, " ", 3); len(s) >= 2 && s[0] == "version" {
			if v, err := strconv.Atoi(s[1]); err == nil && v > 0 {
				p.version = v
			}
		}
	}

	// read version specific flags - extend as necessary
	switch p.version {
	// case 5:
	// 	...
	//	fallthrough
	case 4, 3, 2, 1:
		p.debugFormat = p.rawStringln(p.rawByte()) == "debug"
		p.trackAllTypes = p.int() != 0
		p.posInfoFormat = p.int() != 0
	case 0:
		// Go1.7 encoding format - nothing to do here
	default:
		errorf("unknown export format version %d (%q)", p.version, versionstr)
	}

	// --- generic export data ---

	// populate typList with predeclared "known" types
	p.typList = append(p.typList, predeclared...)

	// read package data
	pkg := p.pkg()

	// read objects of phase 1 only (see cmd/compiler/internal/gc/bexport.go)
	objcount := 0
	for {
		tag := p.tagOrIndex()
		if tag == endTag {
			break
		}
		p.obj(tag)
		objcount++
	}

	// self-verification
	if count := p.int(); count != objcount {
		errorf("got %d objects; want %d", objcount, count)
	}

	// ignore compiler-specific import data

	// complete interfaces
	for _, typ := range p.typList {
		// If we only record named types (!p.trackAllTypes),
		// we must check the underlying types here. If we
		// track all types, the Underlying() method call is
		// not needed.
		// TODO(gri) Remove if p.trackAllTypes is gone.
		if it, ok := typ.Underlying().(*types.Interface); ok {
			it.Complete()
		}
	}

	// record all referenced packages as imports
	list := append(([]*types.Package)(nil), p.pkgList[1:]...)
	sort.Sort(byPath(list))
	pkg.SetImports(list)

	// package was imported completely and without errors
	pkg.MarkComplete()

	return p.read, pkg, nil
}

func errorf(format string, args ...interface{}) {
	panic(fmt.Sprintf(format, args...))
}

func (p *importer) pkg() *types.Package {
	// if the package was seen before, i is its index (>= 0)
	i := p.tagOrIndex()
	if i >= 0 {
		return p.pkgList[i]
	}

	// otherwise, i is the package tag (< 0)
	if i != packageTag {
		errorf("unexpected package tag %d", i)
	}

	// read package data
	name := p.string()
	path := p.string()

	// we should never see an empty package name
	if name == "" {
		errorf("empty package name in import")
	}

	// an empty path denotes the package we are currently importing;
	// it must be the first package we see
	if (path == "") != (len(p.pkgList) == 0) {
		errorf("package path %q for pkg index %d", path, len(p.pkgList))
	}

	// if the package was imported before, use that one; otherwise create a new one
	if path == "" {
		path = p.path
	}
	pkg := p.imports[path]
	if pkg == nil {
		pkg = types.NewPackage(path, name)
		p.imports[path] = pkg
	} else if pkg.Name() != name {
		errorf("conflicting names %s and %s for package %q", pkg.Name(), name, path)
	}
	p.pkgList = append(p.pkgList, pkg)

	return pkg
}

// objTag returns the tag value for each object kind.
func objTag(obj types.Object) int {
	switch obj.(type) {
	case *types.Const:
		return constTag
	case *types.TypeName:
		return typeTag
	case *types.Var:
		return varTag
	case *types.Func:
		return funcTag
	default:
		errorf("unexpected object: %v (%T)", obj, obj) // panics
		panic("unreachable")
	}
}

func sameObj(a, b types.Object) bool {
	// Because unnamed types are not canonicalized, we cannot simply compare types for
	// (pointer) identity.
	// Ideally we'd check equality of constant values as well, but this is good enough.
	return objTag(a) == objTag(b) && types.Identical(a.Type(), b.Type())
}

func (p *importer) declare(obj types.Object) {
	pkg := obj.Pkg()
	if alt := pkg.Scope().Insert(obj); alt != nil {
		// This can only trigger if we import a (non-type) object a second time.
		// Excluding type aliases, this cannot happen because 1) we only import a package
		// once; and b) we ignore compiler-specific export data which may contain
		// functions whose inlined function bodies refer to other functions that
		// were already imported.
		// However, type aliases require reexporting the original type, so we need
		// to allow it (see also the comment in cmd/compile/internal/gc/bimport.go,
		// method importer.obj, switch case importing functions).
		// TODO(gri) review/update this comment once the gc compiler handles type aliases.
		if !sameObj(obj, alt) {
			errorf("inconsistent import:\n\t%v\npreviously imported as:\n\t%v\n", obj, alt)
		}
	}
}

func (p *importer) obj(tag int) {
	switch tag {
	case constTag:
		pos := p.pos()
		pkg, name := p.qualifiedName()
		typ := p.typ(nil)
		val := p.value()
		p.declare(types.NewConst(pos, pkg, name, typ, val))

	case aliasTag:
		// TODO(gri) verify type alias hookup is correct
		pos := p.pos()
		pkg, name := p.qualifiedName()
		typ := p.typ(nil)
		p.declare(types.NewTypeName(pos, pkg, name, typ))

	case typeTag:
		p.typ(nil)

	case varTag:
		pos := p.pos()
		pkg, name := p.qualifiedName()
		typ := p.typ(nil)
		p.declare(types.NewVar(pos, pkg, name, typ))

	case funcTag:
		pos := p.pos()
		pkg, name := p.qualifiedName()
		params, isddd := p.paramList()
		result, _ := p.paramList()
		sig := types.NewSignature(nil, params, result, isddd)
		p.declare(types.NewFunc(pos, pkg, name, sig))

	default:
		errorf("unexpected object tag %d", tag)
	}
}

func (p *importer) pos() token.Pos {
	if !p.posInfoFormat {
		return token.NoPos
	}

	file := p.prevFile
	line := p.prevLine
	if delta := p.int(); delta != 0 {
		// line changed
		line += delta
	} else if n := p.int(); n >= 0 {
		// file changed
		file = p.prevFile[:n] + p.string()
		p.prevFile = file
		line = p.int()
	}
	p.prevLine = line

	// Synthesize a token.Pos

	// Since we don't know the set of needed file positions, we
	// reserve maxlines positions per file.
	const maxlines = 64 * 1024
	f := p.files[file]
	if f == nil {
		f = p.fset.AddFile(file, -1, maxlines)
		p.files[file] = f
		// Allocate the fake linebreak indices on first use.
		// TODO(adonovan): opt: save ~512KB using a more complex scheme?
		fakeLinesOnce.Do(func() {
			fakeLines = make([]int, maxlines)
			for i := range fakeLines {
				fakeLines[i] = i
			}
		})
		f.SetLines(fakeLines)
	}

	if line > maxlines {
		line = 1
	}

	// Treat the file as if it contained only newlines
	// and column=1: use the line number as the offset.
	return f.Pos(line - 1)
}

var (
	fakeLines     []int
	fakeLinesOnce sync.Once
)

func (p *importer) qualifiedName() (pkg *types.Package, name string) {
	name = p.string()
	pkg = p.pkg()
	return
}

func (p *importer) record(t types.Type) {
	p.typList = append(p.typList, t)
}

// A dddSlice is a types.Type representing ...T parameters.
// It only appears for parameter types and does not escape
// the importer.
type dddSlice struct {
	elem types.Type
}

func (t *dddSlice) Underlying() types.Type { return t }
func (t *dddSlice) String() string         { return "..." + t.elem.String() }

// parent is the package which declared the type; parent == nil means
// the package currently imported. The parent package is needed for
// exported struct fields and interface methods which don't contain
// explicit package information in the export data.
func (p *importer) typ(parent *types.Package) types.Type {
	// if the type was seen before, i is its index (>= 0)
	i := p.tagOrIndex()
	if i >= 0 {
		return p.typList[i]
	}

	// otherwise, i is the type tag (< 0)
	switch i {
	case namedTag:
		// read type object
		pos := p.pos()
		parent, name := p.qualifiedName()
		scope := parent.Scope()
		obj := scope.Lookup(name)

		// if the object doesn't exist yet, create and insert it
		if obj == nil {
			obj = types.NewTypeName(pos, parent, name, nil)
			scope.Insert(obj)
		}

		if _, ok := obj.(*types.TypeName); !ok {
			errorf("pkg = %s, name = %s => %s", parent, name, obj)
		}

		// associate new named type with obj if it doesn't exist yet
		t0 := types.NewNamed(obj.(*types.TypeName), nil, nil)

		// but record the existing type, if any
		t := obj.Type().(*types.Named)
		p.record(t)

		// read underlying type
		t0.SetUnderlying(p.typ(parent))

		// interfaces don't have associated methods
		if types.IsInterface(t0) {
			return t
		}

		// read associated methods
		for i := p.int(); i > 0; i-- {
			// TODO(gri) replace this with something closer to fieldName
			pos := p.pos()
			name := p.string()
			if !exported(name) {
				p.pkg()
			}

			recv, _ := p.paramList() // TODO(gri) do we need a full param list for the receiver?
			params, isddd := p.paramList()
			result, _ := p.paramList()
			p.int() // go:nointerface pragma - discarded

			sig := types.NewSignature(recv.At(0), params, result, isddd)
			t0.AddMethod(types.NewFunc(pos, parent, name, sig))
		}

		return t

	case arrayTag:
		t := new(types.Array)
		if p.trackAllTypes {
			p.record(t)
		}

		n := p.int64()
		*t = *types.NewArray(p.typ(parent), n)
		return t

	case sliceTag:
		t := new(types.Slice)
		if p.trackAllTypes {
			p.record(t)
		}

		*t = *types.NewSlice(p.typ(parent))
		return t

	case dddTag:
		t := new(dddSlice)
		if p.trackAllTypes {
			p.record(t)
		}

		t.elem = p.typ(parent)
		return t

	case structTag:
		t := new(types.Struct)
		if p.trackAllTypes {
			p.record(t)
		}

		*t = *types.NewStruct(p.fieldList(parent))
		return t

	case pointerTag:
		t := new(types.Pointer)
		if p.trackAllTypes {
			p.record(t)
		}

		*t = *types.NewPointer(p.typ(parent))
		return t

	case signatureTag:
		t := new(types.Signature)
		if p.trackAllTypes {
			p.record(t)
		}

		params, isddd := p.paramList()
		result, _ := p.paramList()
		*t = *types.NewSignature(nil, params, result, isddd)
		return t

	case interfaceTag:
		// Create a dummy entry in the type list. This is safe because we
		// cannot expect the interface type to appear in a cycle, as any
		// such cycle must contain a named type which would have been
		// first defined earlier.
		n := len(p.typList)
		if p.trackAllTypes {
			p.record(nil)
		}

		// no embedded interfaces with gc compiler
		if p.int() != 0 {
			errorf("unexpected embedded interface")
		}

		t := types.NewInterface(p.methodList(parent), nil)
		if p.trackAllTypes {
			p.typList[n] = t
		}
		return t

	case mapTag:
		t := new(types.Map)
		if p.trackAllTypes {
			p.record(t)
		}

		key := p.typ(parent)
		val := p.typ(parent)
		*t = *types.NewMap(key, val)
		return t

	case chanTag:
		t := new(types.Chan)
		if p.trackAllTypes {
			p.record(t)
		}

		var dir types.ChanDir
		// tag values must match the constants in cmd/compile/internal/gc/go.go
		switch d := p.int(); d {
		case 1 /* Crecv */ :
			dir = types.RecvOnly
		case 2 /* Csend */ :
			dir = types.SendOnly
		case 3 /* Cboth */ :
			dir = types.SendRecv
		default:
			errorf("unexpected channel dir %d", d)
		}
		val := p.typ(parent)
		*t = *types.NewChan(dir, val)
		return t

	default:
		errorf("unexpected type tag %d", i) // panics
		panic("unreachable")
	}
}

func (p *importer) fieldList(parent *types.Package) (fields []*types.Var, tags []string) {
	if n := p.int(); n > 0 {
		fields = make([]*types.Var, n)
		tags = make([]string, n)
		for i := range fields {
			fields[i], tags[i] = p.field(parent)
		}
	}
	return
}

func (p *importer) field(parent *types.Package) (*types.Var, string) {
	pos := p.pos()
	pkg, name, alias := p.fieldName(parent)
	typ := p.typ(parent)
	tag := p.string()

	anonymous := false
	if name == "" {
		// anonymous field - typ must be T or *T and T must be a type name
		switch typ := deref(typ).(type) {
		case *types.Basic: // basic types are named types
			pkg = nil // // objects defined in Universe scope have no package
			name = typ.Name()
		case *types.Named:
			name = typ.Obj().Name()
		default:
			errorf("named base type expected")
		}
		anonymous = true
	} else if alias {
		// anonymous field: we have an explicit name because it's an alias
		anonymous = true
	}

	return types.NewField(pos, pkg, name, typ, anonymous), tag
}

func (p *importer) methodList(parent *types.Package) (methods []*types.Func) {
	if n := p.int(); n > 0 {
		methods = make([]*types.Func, n)
		for i := range methods {
			methods[i] = p.method(parent)
		}
	}
	return
}

func (p *importer) method(parent *types.Package) *types.Func {
	pos := p.pos()
	pkg, name, _ := p.fieldName(parent)
	params, isddd := p.paramList()
	result, _ := p.paramList()
	sig := types.NewSignature(nil, params, result, isddd)
	return types.NewFunc(pos, pkg, name, sig)
}

func (p *importer) fieldName(parent *types.Package) (pkg *types.Package, name string, alias bool) {
	name = p.string()
	pkg = parent
	if pkg == nil {
		// use the imported package instead
		pkg = p.pkgList[0]
	}
	if p.version == 0 && name == "_" {
		// version 0 didn't export a package for _ fields
		return
	}
	switch name {
	case "":
		// 1) field name matches base type name and is exported: nothing to do
	case "?":
		// 2) field name matches base type name and is not exported: need package
		name = ""
		pkg = p.pkg()
	case "@":
		// 3) field name doesn't match type name (alias)
		name = p.string()
		alias = true
		fallthrough
	default:
		if !exported(name) {
			pkg = p.pkg()
		}
	}
	return
}

func (p *importer) paramList() (*types.Tuple, bool) {
	n := p.int()
	if n == 0 {
		return nil, false
	}
	// negative length indicates unnamed parameters
	named := true
	if n < 0 {
		n = -n
		named = false
	}
	// n > 0
	params := make([]*types.Var, n)
	isddd := false
	for i := range params {
		params[i], isddd = p.param(named)
	}
	return types.NewTuple(params...), isddd
}

func (p *importer) param(named bool) (*types.Var, bool) {
	t := p.typ(nil)
	td, isddd := t.(*dddSlice)
	if isddd {
		t = types.NewSlice(td.elem)
	}

	var pkg *types.Package
	var name string
	if named {
		name = p.string()
		if name == "" {
			errorf("expected named parameter")
		}
		if name != "_" {
			pkg = p.pkg()
		}
		if i := strings.Index(name, "??"); i > 0 {
			name = name[:i] // cut off gc-specific parameter numbering
		}
	}

	// read and discard compiler-specific info
	p.string()

	return types.NewVar(token.NoPos, pkg, name, t), isddd
}

func exported(name string) bool {
	ch, _ := utf8.DecodeRuneInString(name)
	return unicode.IsUpper(ch)
}

func (p *importer) value() constant.Value {
	switch tag := p.tagOrIndex(); tag {
	case falseTag:
		return constant.MakeBool(false)
	case trueTag:
		return constant.MakeBool(true)
	case int64Tag:
		return constant.MakeInt64(p.int64())
	case floatTag:
		return p.float()
	case complexTag:
		re := p.float()
		im := p.float()
		return constant.BinaryOp(re, token.ADD, constant.MakeImag(im))
	case stringTag:
		return constant.MakeString(p.string())
	case unknownTag:
		return constant.MakeUnknown()
	default:
		errorf("unexpected value tag %d", tag) // panics
		panic("unreachable")
	}
}

func (p *importer) float() constant.Value {
	sign := p.int()
	if sign == 0 {
		return constant.MakeInt64(0)
	}

	exp := p.int()
	mant := []byte(p.string()) // big endian

	// remove leading 0's if any
	for len(mant) > 0 && mant[0] == 0 {
		mant = mant[1:]
	}

	// convert to little endian
	// TODO(gri) go/constant should have a more direct conversion function
	//           (e.g., once it supports a big.Float based implementation)
	for i, j := 0, len(mant)-1; i < j; i, j = i+1, j-1 {
		mant[i], mant[j] = mant[j], mant[i]
	}

	// adjust exponent (constant.MakeFromBytes creates an integer value,
	// but mant represents the mantissa bits such that 0.5 <= mant < 1.0)
	exp -= len(mant) << 3
	if len(mant) > 0 {
		for msd := mant[len(mant)-1]; msd&0x80 == 0; msd <<= 1 {
			exp++
		}
	}

	x := constant.MakeFromBytes(mant)
	switch {
	case exp < 0:
		d := constant.Shift(constant.MakeInt64(1), token.SHL, uint(-exp))
		x = constant.BinaryOp(x, token.QUO, d)
	case exp > 0:
		x = constant.Shift(x, token.SHL, uint(exp))
	}

	if sign < 0 {
		x = constant.UnaryOp(token.SUB, x, 0)
	}
	return x
}

// ----------------------------------------------------------------------------
// Low-level decoders

func (p *importer) tagOrIndex() int {
	if p.debugFormat {
		p.marker('t')
	}

	return int(p.rawInt64())
}

func (p *importer) int() int {
	x := p.int64()
	if int64(int(x)) != x {
		errorf("exported integer too large")
	}
	return int(x)
}

func (p *importer) int64() int64 {
	if p.debugFormat {
		p.marker('i')
	}

	return p.rawInt64()
}

func (p *importer) string() string {
	if p.debugFormat {
		p.marker('s')
	}
	// if the string was seen before, i is its index (>= 0)
	// (the empty string is at index 0)
	i := p.rawInt64()
	if i >= 0 {
		return p.strList[i]
	}
	// otherwise, i is the negative string length (< 0)
	if n := int(-i); n <= cap(p.buf) {
		p.buf = p.buf[:n]
	} else {
		p.buf = make([]byte, n)
	}
	for i := range p.buf {
		p.buf[i] = p.rawByte()
	}
	s := string(p.buf)
	p.strList = append(p.strList, s)
	return s
}

func (p *importer) marker(want byte) {
	if got := p.rawByte(); got != want {
		errorf("incorrect marker: got %c; want %c (pos = %d)", got, want, p.read)
	}

	pos := p.read
	if n := int(p.rawInt64()); n != pos {
		errorf("incorrect position: got %d; want %d", n, pos)
	}
}

// rawInt64 should only be used by low-level decoders.
func (p *importer) rawInt64() int64 {
	i, err := binary.ReadVarint(p)
	if err != nil {
		errorf("read error: %v", err)
	}
	return i
}

// rawStringln should only be used to read the initial version string.
func (p *importer) rawStringln(b byte) string {
	p.buf = p.buf[:0]
	for b != '\n' {
		p.buf = append(p.buf, b)
		b = p.rawByte()
	}
	return string(p.buf)
}

// needed for binary.ReadVarint in rawInt64
func (p *importer) ReadByte() (byte, error) {
	return p.rawByte(), nil
}

// byte is the bottleneck interface for reading p.data.
// It unescapes '|' 'S' to '$' and '|' '|' to '|'.
// rawByte should only be used by low-level decoders.
func (p *importer) rawByte() byte {
	b := p.data[0]
	r := 1
	if b == '|' {
		b = p.data[1]
		r = 2
		switch b {
		case 'S':
			b = '$'
		case '|':
			// nothing to do
		default:
			errorf("unexpected escape sequence in export data")
		}
	}
	p.data = p.data[r:]
	p.read += r
	return b

}

// ----------------------------------------------------------------------------
// Export format

// Tags. Must be < 0.
const (
	// Objects
	packageTag = -(iota + 1)
	constTag
	typeTag
	varTag
	funcTag
	endTag

	// Types
	namedTag
	arrayTag
	sliceTag
	dddTag
	structTag
	pointerTag
	signatureTag
	interfaceTag
	mapTag
	chanTag

	// Values
	falseTag
	trueTag
	int64Tag
	floatTag
	fractionTag // not used by gc
	complexTag
	stringTag
	nilTag     // only used by gc (appears in exported inlined function bodies)
	unknownTag // not used by gc (only appears in packages with errors)

	// Type aliases
	aliasTag
)

var predeclared = []types.Type{
	// basic types
	types.Typ[types.Bool],
	types.Typ[types.Int],
	types.Typ[types.Int8],
	types.Typ[types.Int16],
	types.Typ[types.Int32],
	types.Typ[types.Int64],
	types.Typ[types.Uint],
	types.Typ[types.Uint8],
	types.Typ[types.Uint16],
	types.Typ[types.Uint32],
	types.Typ[types.Uint64],
	types.Typ[types.Uintptr],
	types.Typ[types.Float32],
	types.Typ[types.Float64],
	types.Typ[types.Complex64],
	types.Typ[types.Complex128],
	types.Typ[types.String],

	// basic type aliases
	types.Universe.Lookup("byte").Type(),
	types.Universe.Lookup("rune").Type(),

	// error
	types.Universe.Lookup("error").Type(),

	// untyped types
	types.Typ[types.UntypedBool],
	types.Typ[types.UntypedInt],
	types.Typ[types.UntypedRune],
	types.Typ[types.UntypedFloat],
	types.Typ[types.UntypedComplex],
	types.Typ[types.UntypedString],
	types.Typ[types.UntypedNil],

	// package unsafe
	types.Typ[types.UnsafePointer],

	// invalid type
	types.Typ[types.Invalid], // only appears in packages with errors

	// used internally by gc; never used by this package or in .a files
	anyType{},
}

type anyType struct{}

func (t anyType) Underlying() types.Type { return t }
func (t anyType) String() string         { return "any" }
