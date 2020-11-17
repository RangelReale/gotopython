package compiler

import (
	"go/ast"
	"go/token"
	"go/types"

	py "github.com/RangelReale/gotopython/pythonast"
)

var pySelf = py.Identifier("self")

type Module struct {
	Imports   []py.Stmt
	Values    []py.Stmt
	Classes   []*py.ClassDef
	Types     []py.Stmt
	Functions []*py.FunctionDef
	Methods   map[py.Identifier][]*py.FunctionDef
}

type Compiler struct {
	*XCompiler
}

func NewCompiler(typeInfo *types.Info, fileSet *token.FileSet) *Compiler {
	return &Compiler{XCompiler: NewXCompiler(typeInfo, fileSet, true)}
}

func (c Compiler) withCommentMap(cmap *ast.CommentMap) *Compiler {
	c.commentMap = cmap
	return &c
}

func (c *Compiler) newModule() *Module {
	return &Module{Methods: map[py.Identifier][]*py.FunctionDef{}}
}

func (c *Compiler) compileImportSpec(spec *ast.ImportSpec, module *Module) {
	//TODO
}

func (c *Compiler) compileGenDecl(decl *ast.GenDecl, module *Module) {
	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			compiled := c.CompileTypeSpec(s)
			if classDef, ok := compiled.(*py.ClassDef); ok {
				module.Classes = append(module.Classes, classDef)
			} else {
				module.Types = append(module.Types, compiled)
			}
		case *ast.ImportSpec:
			c.compileImportSpec(s, module)
		case *ast.ValueSpec:
			module.Values = append(module.Values, c.CompileValueSpec(s)...)
		default:
			c.err(s, "unknown Spec: %T", s)
		}
	}
}

func (c *Compiler) compileDecl(decl ast.Decl, module *Module) {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		funcDecl := c.CompileFuncDecl(d)
		if funcDecl.Class != py.Identifier("") {
			module.Methods[funcDecl.Class] = append(module.Methods[funcDecl.Class], funcDecl.Def)
		} else {
			module.Functions = append(module.Functions, funcDecl.Def)
		}
	case *ast.GenDecl:
		c.compileGenDecl(d, module)
	default:
		panic(c.err(decl, "unknown Decl: %T", decl))
	}
}

func (c *Compiler) compileFile(file *ast.File, module *Module) {
	cmap := ast.NewCommentMap(c.FileSet, file, file.Comments)
	c1 := c.withCommentMap(&cmap)
	for _, decl := range file.Decls {
		c1.compileDecl(decl, module)
	}
}

func (c *Compiler) CompileFiles(files []*ast.File) *py.Module {
	module := &Module{Methods: map[py.Identifier][]*py.FunctionDef{}}
	for _, file := range files {
		c.compileFile(file, module)
	}
	pyModule := &py.Module{}
	pyModule.Body = append(pyModule.Body, module.Values...)
	for _, class := range module.Classes {
		for _, method := range module.Methods[class.Name] {
			class.Body = append(class.Body, method)
		}
		pyModule.Body = append(pyModule.Body, class)
	}
	for _, fun := range module.Functions {
		pyModule.Body = append(pyModule.Body, fun)
	}
	return pyModule
}
