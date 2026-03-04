package hooks_yaegi

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/token"
	"strconv"
	"strings"

	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

type HookFunc func(params map[string]any, bar map[string]any) (map[string]any, error)

type Sandbox struct {
	goPath      string
	allowedPkgs map[string]struct{}
	blocked     map[string]struct{}
}

type SandboxOption func(*Sandbox)

func WithGoPath(goPath string) SandboxOption {
	return func(s *Sandbox) {
		s.goPath = goPath
	}
}

func NewSandbox(opts ...SandboxOption) *Sandbox {
	allowed := map[string]struct{}{
		"math":    {},
		"fmt":     {},
		"strings": {},
		"strconv": {},
		"sort":    {},
		"errors":  {},
	}

	blocked := map[string]struct{}{
		"os":        {},
		"net":       {},
		"runtime":   {},
		"reflect":   {},
		"unsafe":    {},
		"syscall":   {},
		"os/exec":   {},
		"net/http":  {},
		"io/ioutil": {},
		"plugin":    {},
	}

	s := &Sandbox{
		goPath:      build.Default.GOPATH,
		allowedPkgs: allowed,
		blocked:     blocked,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

func (s *Sandbox) Compile(name string, source string) (HookFunc, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("yaegi sandbox: hook name is required")
	}

	file, err := parseSingleFile(source)
	if err != nil {
		return nil, err
	}
	if file.Name == nil || file.Name.Name != "hookpkg" {
		return nil, fmt.Errorf("yaegi sandbox: source must declare package hookpkg")
	}
	if err := s.validateImports(file); err != nil {
		return nil, err
	}

	i := interp.New(interp.Options{GoPath: s.goPath})
	if err := i.Use(filteredStdlibSymbols(s.allowedPkgs)); err != nil {
		return nil, fmt.Errorf("yaegi sandbox: failed to load stdlib symbols: %w", err)
	}
	if _, err := i.Eval(source); err != nil {
		return nil, fmt.Errorf("yaegi sandbox: eval error: %w", err)
	}

	v, err := i.Eval("hookpkg." + name)
	if err != nil {
		return nil, fmt.Errorf("yaegi sandbox: hook symbol not found %q: %w", "hookpkg."+name, err)
	}

	fn, ok := v.Interface().(func(map[string]any, map[string]any) (map[string]any, error))
	if !ok {
		return nil, fmt.Errorf("yaegi sandbox: hook %q has wrong signature, got %T", name, v.Interface())
	}

	return HookFunc(fn), nil
}

func parseSingleFile(src string) (*ast.File, error) {
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, "hook.go", src, parser.ImportsOnly)
	if err != nil {
		return nil, fmt.Errorf("yaegi sandbox: parse error: %w", err)
	}
	return file, nil
}

func (s *Sandbox) validateImports(file *ast.File) error {
	for _, imp := range file.Imports {
		if imp == nil || imp.Path == nil {
			continue
		}
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return fmt.Errorf("yaegi sandbox: invalid import path %q: %w", imp.Path.Value, err)
		}

		if _, isBlocked := s.blocked[p]; isBlocked {
			return fmt.Errorf("%w: import %q is not allowed", ErrBlockedImport, p)
		}
		if _, ok := s.allowedPkgs[p]; !ok {
			return fmt.Errorf("%w: import %q is not allowed", ErrBlockedImport, p)
		}
	}
	return nil
}

func filteredStdlibSymbols(allowedPkgs map[string]struct{}) interp.Exports {
	filtered := make(interp.Exports)
	for path, symbols := range stdlib.Symbols {
		root := path
		if idx := strings.IndexByte(path, '/'); idx >= 0 {
			root = path[:idx]
		}
		if _, ok := allowedPkgs[root]; !ok {
			continue
		}
		filtered[path] = symbols
	}
	return filtered
}
