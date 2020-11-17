package compiler

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strconv"
	"strings"

	py "github.com/RangelReale/gotopython/pythonast"
)

type XCompiler struct {
	*types.Info
	*scope
	*token.FileSet
	commentMap *ast.CommentMap
	defers     py.Expr
	global     bool
}

func NewXCompiler(typeInfo *types.Info, fileSet *token.FileSet, global bool) *XCompiler {
	return &XCompiler{Info: typeInfo, scope: newScope(), FileSet: fileSet, global: global}
}

func (c XCompiler) nestedCompiler() *XCompiler {
	c.scope = c.scope.nested()
	return &c
}

func (c *XCompiler) exprCompiler() *exprCompiler {
	return &exprCompiler{XCompiler: c}
}

func (c *XCompiler) err(node ast.Node, msg string, args ...interface{}) string {
	if c.FileSet != nil {
		return fmt.Sprintf("%s: %s", c.Position(node.Pos()), fmt.Sprintf(msg, args...))
	} else {
		return fmt.Sprintf(msg, args...)
	}
}

func (c *XCompiler) identifier(ident *ast.Ident) py.Identifier {
	return c.objID(c.ObjectOf(ident))
}

func (c *XCompiler) fieldType(field *ast.Field) py.Identifier {
	var ident *ast.Ident
	switch e := field.Type.(type) {
	case *ast.StarExpr:
		ident = e.X.(*ast.Ident)
	case *ast.Ident:
		ident = e
	default:
		panic(c.err(field, "unknown field type: %T", field.Type))
	}
	return c.identifier(ident)
}

type FuncDecl struct {
	Class py.Identifier // "" if free function
	Def   *py.FunctionDef
}

func (c *XCompiler) addDefers(body *ast.BlockStmt) py.Stmt {
	if !c.global {
		return nil
	}

	hasDefer := false
	ast.Inspect(body, func(node ast.Node) bool {
		// Break early if a defer was found
		if hasDefer {
			return false
		}
		switch node.(type) {
		case *ast.DeferStmt:
			c.defers = &py.Name{Id: c.tempID("defers")}
			hasDefer = true
			return false
		case ast.Stmt:
			// Recurse into if, for, etc
			return true
		}
		// Do not recurse into expressions e.g. function literals
		return false
	})
	if hasDefer {
		return &py.Assign{
			Targets: []py.Expr{c.defers},
			Value:   &py.List{},
		}
	}
	return nil
}

func (parent *XCompiler) CompileFunc(name py.Identifier, typ *ast.FuncType, body *ast.BlockStmt, isMethod bool, recv *ast.Ident) *py.FunctionDef {
	pyArgs := py.Arguments{}
	// Compiler with nested function scope
	c := parent.nestedCompiler()

	var pyBody []py.Stmt

	// add an empty list of defer functions before the function body if this function uses defer
	var deferInit py.Stmt
	if parent.global {
		deferInit = c.addDefers(body)
	}

	if isMethod {
		var recvId py.Identifier
		if recv != nil {
			recvId = c.identifier(recv)
		} else {
			recvId = c.tempID("self")
		}
		pyArgs.Args = append(pyArgs.Args, py.Arg{Arg: recvId})
	}
	for _, param := range typ.Params.List {
		for _, name := range param.Names {
			pyArgs.Args = append(pyArgs.Args, py.Arg{Arg: c.identifier(name)})
		}
	}

	for _, stmt := range body.List {
		pyBody = append(pyBody, c.compileStmt(stmt)...)
	}

	if parent.global {
		// Execute defers
		if deferInit != nil {
			fun := &py.Name{Id: c.tempID("fun")}
			args := &py.Name{Id: c.tempID("args")}
			forLoop := &py.For{
				Target: makeTuple(fun, args),
				Iter:   &py.Call{Func: pyReversed, Args: []py.Expr{c.defers}},
				Body: []py.Stmt{
					&py.ExprStmt{
						Value: &py.Call{Func: fun, Args: []py.Expr{&py.Starred{Value: args}}},
					},
				},
			}
			pyBody = []py.Stmt{
				deferInit,
				&py.Try{
					Body:      pyBody,
					Finalbody: []py.Stmt{forLoop},
				},
			}
		}
	}

	if len(pyBody) == 0 {
		pyBody = []py.Stmt{&py.Pass{}}
	}
	return &py.FunctionDef{Name: name, Args: pyArgs, Body: pyBody}
}

func makeDocString(g *ast.CommentGroup) *py.DocString {
	text := g.Text()
	text = strings.TrimRight(text, "\n")
	return &py.DocString{Lines: strings.Split(text, "\n")}
}

func (c *XCompiler) CompileFuncDecl(decl *ast.FuncDecl, withDoc bool) FuncDecl {
	var recvType py.Identifier
	var recv *ast.Ident
	if decl.Recv != nil {
		if len(decl.Recv.List) > 1 || len(decl.Recv.List[0].Names) > 1 {
			panic(c.err(decl, "multiple receivers"))
		}
		field := decl.Recv.List[0]
		if len(field.Names) == 1 {
			recv = field.Names[0]
		}
		recvType = c.fieldType(field)
	}
	funcDef := c.CompileFunc(c.identifier(decl.Name), decl.Type, decl.Body, decl.Recv != nil, recv)

	if withDoc && decl.Doc != nil {
		funcDef.Body = append([]py.Stmt{makeDocString(decl.Doc)}, funcDef.Body...)
	}
	return FuncDecl{Class: recvType, Def: funcDef}
}

func (c *XCompiler) zeroValue(typ types.Type) py.Expr {
	switch t := typ.(type) {
	case *types.Pointer, *types.Slice, *types.Map, *types.Signature, *types.Interface, *types.Struct, *types.Chan:
		return pyNone
	case *types.Basic:
		switch {
		case t.Info()&types.IsString != 0:
			return &py.Str{S: "\"\""}
		case t.Info()&types.IsBoolean != 0:
			return &py.NameConstant{Value: py.False}
		case t.Info()&types.IsInteger != 0:
			return &py.Num{N: "0"}
		case t.Info()&types.IsFloat != 0:
			return &py.Num{N: "0.0"}
		case t.Info()&types.IsComplex != 0:
			return &py.Num{N: "0.0"}
		default:
			panic(fmt.Sprintf("unknown basic type %#v", t))
		}
	case *types.Named:
		return &py.Call{Func: &py.Name{Id: py.Identifier(t.Obj().Name())}}
	case *types.Array:
		return &py.ListComp{
			Elt: c.zeroValue(t.Elem()),
			Generators: []py.Comprehension{
				py.Comprehension{
					Target: &py.Name{Id: py.Identifier("_")},
					Iter: &py.Call{
						Func: pyRange,
						Args: []py.Expr{&py.Num{N: strconv.FormatInt(t.Len(), 10)}},
					},
				},
			},
		}
	default:
		panic(fmt.Sprintf("unknown zero value for %T", t))
	}
}

func (c *XCompiler) makeInitMethod(typ *types.Struct) *py.FunctionDef {
	nested := c.nestedCompiler()
	args := []py.Arg{py.Arg{Arg: pySelf}}
	var defaults []py.Expr
	for i := 0; i < typ.NumFields(); i++ {
		field := typ.Field(i)
		arg := py.Arg{Arg: nested.objID(field)}
		args = append(args, arg)
		dflt := nested.zeroValue(field.Type())
		defaults = append(defaults, dflt)
	}

	var body []py.Stmt
	for i := 0; i < typ.NumFields(); i++ {
		field := typ.Field(i)
		assign := &py.Assign{
			Targets: []py.Expr{
				&py.Attribute{
					Value: &py.Name{Id: pySelf},
					Attr:  nested.objID(field),
				},
			},
			Value: &py.Name{Id: nested.objID(field)},
		}
		body = append(body, assign)
	}
	initMethod := &py.FunctionDef{
		Name: py.Identifier("__init__"),
		Args: py.Arguments{Args: args, Defaults: defaults},
		Body: body,
	}
	return initMethod
}

func (c *XCompiler) compileStructType(ident *ast.Ident, typ *types.Struct) *py.ClassDef {
	var body []py.Stmt

	if c.global {
		if c.commentMap != nil {
			doc := (*c.commentMap)[ident]
			if len(doc) > 0 {
				body = append(body, makeDocString(doc[0]))
			}
		}

		if typ.NumFields() > 0 {
			body = append(body, c.makeInitMethod(typ))
		}
	}

	if len(body) == 0 {
		body = []py.Stmt{&py.Pass{}}
	}
	return &py.ClassDef{
		Name:          c.identifier(ident),
		Bases:         nil,
		Keywords:      nil,
		Body:          body,
		DecoratorList: nil,
	}
}

func (c *XCompiler) compileInterfaceType(ident *ast.Ident, typ *types.Interface) py.Stmt {
	return nil
}

func (c *XCompiler) compileSignature(ident *ast.Ident, typ *types.Signature) py.Stmt {
	return nil
}

func (c *XCompiler) compileMapType(ident *ast.Ident, typ *types.Map) py.Stmt {
	return nil
}

func (c *XCompiler) CompileTypeSpec(spec *ast.TypeSpec) py.Stmt {
	switch t := c.TypeOf(spec.Type).(type) {
	case *types.Struct:
		return c.compileStructType(spec.Name, t)
	case *types.Named:
		return &py.Assign{
			Targets: []py.Expr{&py.Name{Id: c.identifier(spec.Name)}},
			Value:   &py.Name{Id: c.objID(t.Obj())},
		}
	case *types.Interface:
		return c.compileInterfaceType(spec.Name, t)
	case *types.Basic, *types.Slice:
		fields := []*types.Var{types.NewField(token.NoPos, nil, "value", t, false)}
		return c.compileStructType(spec.Name, types.NewStruct(fields, nil))
	case *types.Signature:
		return c.compileSignature(spec.Name, t)
	case *types.Map:
		return c.compileMapType(spec.Name, t)
	default:
		panic(c.err(spec, "unknown TypeSpec: %T", t))
	}
}

func (c *XCompiler) CompileImportSpec(spec *ast.ImportSpec) py.Stmt {
	//TODO
	return nil
}

func (c *XCompiler) CompileGenDecl(decl *ast.GenDecl) []py.Stmt {
	var ret []py.Stmt
	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			ret = append(ret, c.CompileTypeSpec(s))
		case *ast.ImportSpec:
			ret = append(ret, c.CompileImportSpec(s))
		case *ast.ValueSpec:
			ret = append(ret, c.CompileValueSpec(s)...)
		default:
			c.err(s, "unknown Spec: %T", s)
		}
	}
	return ret
}

func (c *XCompiler) CompileDecl(decl ast.Decl) []py.Stmt {
	var ret []py.Stmt

	switch d := decl.(type) {
	case *ast.FuncDecl:
		funcDecl := c.CompileFuncDecl(d, true)
		ret = append(ret, funcDecl.Def)
	case *ast.GenDecl:
		ret = append(ret, c.CompileGenDecl(d)...)
	default:
		panic(c.err(decl, "unknown Decl: %T", decl))
	}

	return ret
}
