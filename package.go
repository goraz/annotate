package humanize

import (
	"fmt"
	"go/ast"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Package is list of files
type Package struct {
	Files []File
	Path  string
	Name  string
}

var (
	packageCache map[string]*Package
)

// FindType return a base type interface base on the string name of the type
func (p Package) FindType(t string) (*TypeName, error) {
	for i := range p.Files {
		for j := range p.Files[i].Types {
			if p.Files[i].Types[j].Name == t {
				return p.Files[i].Types[j], nil
			}
		}
	}

	return nil, fmt.Errorf("type with name %s not found", t)
}

// FindVariable try to find a package level variable
func (p Package) FindVariable(t string) (*Variable, error) {
	for i := range p.Files {
		for j := range p.Files[i].Variables {
			if p.Files[i].Variables[j].Name == t {
				return p.Files[i].Variables[j], nil
			}
		}
	}

	return nil, fmt.Errorf("var with name %s not found", t)
}

// FindConstant try to find a package level variable
func (p Package) FindConstant(t string) (*Constant, error) {
	for i := range p.Files {
		for j := range p.Files[i].Constants {
			if p.Files[i].Constants[j].Name == t {
				return p.Files[i].Constants[j], nil
			}
		}
	}

	return nil, fmt.Errorf("const with name %s not found", t)
}

// FindFunction try to find a package level variable
func (p Package) FindFunction(t string) (*Function, error) {
	for i := range p.Files {
		for j := range p.Files[i].Functions {
			if p.Files[i].Functions[j].Name == t {
				return p.Files[i].Functions[j], nil
			}
		}
	}
	return nil, fmt.Errorf("func with name %s not found", t)
}

// FindImport try to find an import by its full import path
func (p Package) FindImport(t string) (*Import, error) {
	if t == "" || t == "_" || t == "." {
		return nil, fmt.Errorf("import with path _/. or empty is invalid")
	}
	for i := range p.Files {
		for j := range p.Files[i].Imports {
			if p.Files[i].Imports[j].Name == t || p.Files[i].Imports[j].Path == t {
				return p.Files[i].Imports[j], nil
			}
		}
	}

	return nil, fmt.Errorf("import with name or path %s not found", t)
}

func translateToFullPath(path string) (string, error) {
	root := runtime.GOROOT()
	gopath := strings.Split(os.Getenv("GOPATH"), ":")

	test := filepath.Join(root, "src", path)
	r, err := os.Stat(test)
	if err != nil {
		for i := range gopath {
			test = filepath.Join(gopath[i], "src", path)
			r, err = os.Stat(test)
			if err == nil {
				break
			}
		}
		if err != nil {
			return "", fmt.Errorf("%s is not found in GOROOT or GOPATH", path)
		}
	}

	if !r.IsDir() {
		return "", fmt.Errorf("%s is found in %s but its not a directory", path, r.Name())
	}

	return test, nil
}

func lateBind(p *Package) (res error) {
	for f := range p.Files {
		// Try to find variable with null type and change them to real type
		thebigLoop:
		for v := range p.Files[f].Variables {
			if p.Files[f].Variables[v].caller != nil {
				switch c := p.Files[f].Variables[v].caller.Fun.(type) {
				case *ast.Ident:
					name := nameFromIdent(c)
					// TODO : list all builtin functions?
					if name == "make" {
						p.Files[f].Variables[v].Type = getType(p.Files[f].Variables[v].caller.Args[0], "")
					} else {
						fn, err := p.FindFunction(name)
						if err != nil {
							return err
						}

						if len(fn.Type.Results) <= p.Files[f].Variables[v].indx {
							return fmt.Errorf("%d result is available but want the %d", len(fn.Type.Results), p.Files[f].Variables[v].indx)
						}
						p.Files[f].Variables[v].Type = fn.Type.Results[p.Files[f].Variables[v].indx].Type
					}
				case *ast.SelectorExpr:
					var pkg string
					switch c.X.(type) {
					case *ast.Ident:
						pkg = nameFromIdent(c.X.(*ast.Ident))
					case *ast.CallExpr: // TODO : Don't know why, no time for check
						continue thebigLoop;
					}

					typ := nameFromIdent(c.Sel)
					imprt, err := p.FindImport(pkg)
					if err != nil {
						return err
					}
					pkgDef, err := ParsePackage(imprt.Path)
					if err != nil {
						return err
					}
					fn, err := pkgDef.FindFunction(typ)
					if err != nil {
						return err
					}

					if len(fn.Type.Results) <= p.Files[f].Variables[v].indx {
						return fmt.Errorf("%d result is available but want the %d", len(fn.Type.Results), p.Files[f].Variables[v].indx)
					}
					foreignTyp := fn.Type.Results[p.Files[f].Variables[v].indx].Type
					star := false
					if sType, ok := foreignTyp.(*StarType); ok {
						foreignTyp = sType.Target
						star = true
					}
					switch ft := foreignTyp.(type) {
					case *IdentType:
						// this is a simple hack. if the type is begin with
						// upper case, then its type on that package, else its a global type
						name := ft.Ident
						c := name[0]
						if c >= 'A' && c <= 'Z' {
							if star {
								foreignTyp = &StarType{
									ft.srcBase,
									foreignTyp,
								}
							}
							p.Files[f].Variables[v].Type = &SelectorType{
								srcBase: srcBase{""}, // TODO : source?
								Package: imprt.Name,
								Type:    foreignTyp,
							}
						} else {
							if star {
								foreignTyp = &StarType{
									ft.srcBase,
									foreignTyp,
								}
							}
							p.Files[f].Variables[v].Type = foreignTyp
						}

					default:
						// the type is foreign to that package too
						p.Files[f].Variables[v].Type = ft
					}
				}
			}
		}
	}
	return nil
}

// ParsePackage is here for loading a single package and parse all files in it
func ParsePackage(path string) (*Package, error) {
	if p, ok := packageCache[path]; ok {
		return p, nil
	}
	var p = &Package{}
	p.Path = path
	folder, err := translateToFullPath(path)
	if err != nil {
		return nil, err
	}
	err = filepath.Walk(
		folder,
		func(path string, f os.FileInfo, err error) error {
			if f.IsDir() {
				return nil
			}
			// ignore test files (for now?)
			_, filename := filepath.Split(path)
			if len(filename) > 8 && filename[len(filename)-8:] == "_test.go" {
				return nil
			}
			if filepath.Ext(path) != ".go" {
				return nil
			}
			r, err := os.Open(path)
			if err != nil {
				return err
			}
			defer r.Close()

			data, err := ioutil.ReadAll(r)
			if err != nil {
				return err
			}

			fl, err := ParseFile(string(data))
			if err != nil {
				return err
			}
			fl.FileName = path
			p.Files = append(p.Files, fl)
			p.Name = fl.PackageName

			return nil
		},
	)
	if err != nil {
		return nil, err
	}
	packageCache[path] = p

	err = lateBind(p)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func init() {
	packageCache = make(map[string]*Package)
}
