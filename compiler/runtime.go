package compiler

import py "github.com/RangelReale/gotopython/pythonast"

var (
	runtimeModule = &py.Name{Id: py.Identifier("runtime")}
)
