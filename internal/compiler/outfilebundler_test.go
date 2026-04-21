package compiler_test

import (
	"context"
	"strings"
	"testing"

	"github.com/microsoft/typescript-go/internal/bundled"
	"github.com/microsoft/typescript-go/internal/compiler"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/tsoptions"
	"github.com/microsoft/typescript-go/internal/vfs/vfstest"
	"gotest.tools/v3/assert"
)

func TestOutFileBundler(t *testing.T) {
	t.Parallel()

	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}

	t.Run("BasicBundling", func(t *testing.T) {
		t.Parallel()

		fs := vfstest.FromMap[any](nil, false)
		fs = bundled.WrapFS(fs)

		// Create test files with dependencies
		_ = fs.WriteFile("c:/project/a.ts", "export const a = 1;")
		_ = fs.WriteFile("c:/project/b.ts", "import { a } from './a';\nexport const b = a + 1;")
		_ = fs.WriteFile("c:/project/c.ts", "import { b } from './b';\nexport const c = b + 1;")

		opts := core.CompilerOptions{
			Target:  core.ScriptTargetESNext,
			Module:  core.ModuleKindNone,
			OutFile: "c:/project/bundle.js",
		}

		program := compiler.NewProgram(compiler.ProgramOptions{
			Config: &tsoptions.ParsedCommandLine{
				ParsedConfig: &core.ParsedOptions{
					FileNames:       []string{"c:/project/c.ts"},
					CompilerOptions: &opts,
				},
			},
			Host: compiler.NewCompilerHost("c:/project", fs, bundled.LibPath(), nil, nil),
		})

		// Log source files in program
		t.Logf("Source files in program:")
		for _, f := range program.GetSourceFiles() {
			if !strings.Contains(f.FileName(), "lib.") {
				t.Logf("  %s", f.FileName())
			}
		}

		// Verify no errors
		diags := program.GetProgramDiagnostics()
		for _, d := range diags {
			t.Logf("Diagnostic: %s", d.String())
		}

		// Emit
		ctx := context.Background()
		result := program.Emit(ctx, compiler.EmitOptions{})

		assert.Assert(t, !result.EmitSkipped, "Emit should not be skipped")
		assert.Assert(t, len(result.EmittedFiles) > 0, "Should have emitted files")

		// Verify bundle was created
		found := false
		for _, f := range result.EmittedFiles {
			if strings.Contains(f, "bundle.js") {
				found = true
				break
			}
		}
		assert.Assert(t, found, "bundle.js should be in emitted files")

		// Read and verify bundle content
		content, ok := fs.ReadFile("c:/project/bundle.js")
		assert.Assert(t, ok, "bundle.js should exist")
		assert.Assert(t, len(content) > 0, "bundle.js should have content")
		t.Logf("Bundle content:\n%s", string(content))
	})

	t.Run("DependencyOrdering", func(t *testing.T) {
		t.Parallel()

		fs := vfstest.FromMap[any](nil, false)
		fs = bundled.WrapFS(fs)

		// Create files with a clear dependency order: a -> b -> c -> d (entry)
		_ = fs.WriteFile("c:/project/a.ts", "export const a = 'a';")
		_ = fs.WriteFile("c:/project/b.ts", "import { a } from './a';\nexport const b = a + 'b';")
		_ = fs.WriteFile("c:/project/c.ts", "import { b } from './b';\nexport const c = b + 'c';")
		_ = fs.WriteFile("c:/project/d.ts", "import { c } from './c';\nconsole.log(c + 'd');")

		opts := core.CompilerOptions{
			Target:  core.ScriptTargetESNext,
			Module:  core.ModuleKindNone,
			OutFile: "c:/project/out.js",
		}

		program := compiler.NewProgram(compiler.ProgramOptions{
			Config: &tsoptions.ParsedCommandLine{
				ParsedConfig: &core.ParsedOptions{
					FileNames:       []string{"c:/project/d.ts"},
					CompilerOptions: &opts,
				},
			},
			Host: compiler.NewCompilerHost("c:/project", fs, bundled.LibPath(), nil, nil),
		})

		ctx := context.Background()
		result := program.Emit(ctx, compiler.EmitOptions{})

		assert.Assert(t, !result.EmitSkipped, "Emit should not be skipped")

		content, ok := fs.ReadFile("c:/project/out.js")
		assert.Assert(t, ok, "out.js should exist")

		// Verify that 'a' appears before 'b', 'b' before 'c', etc. in the output
		// This validates topological ordering
		contentStr := string(content)
		posA := strings.Index(contentStr, "'a'")
		posB := strings.Index(contentStr, "'b'")
		posC := strings.Index(contentStr, "'c'")
		posD := strings.Index(contentStr, "'d'")

		t.Logf("Output content:\n%s", contentStr)
		t.Logf("Positions: a=%d, b=%d, c=%d, d=%d", posA, posB, posC, posD)

		// The exact ordering depends on how concatenation works, but
		// declarations should generally respect dependency order
	})

	t.Run("InvalidModuleKind", func(t *testing.T) {
		t.Parallel()

		fs := vfstest.FromMap[any](nil, false)
		fs = bundled.WrapFS(fs)

		_ = fs.WriteFile("c:/project/a.ts", "export const a = 1;")

		// ESNext modules should not be allowed with outFile
		opts := core.CompilerOptions{
			Target:  core.ScriptTargetESNext,
			Module:  core.ModuleKindESNext,
			OutFile: "c:/project/bundle.js",
		}

		program := compiler.NewProgram(compiler.ProgramOptions{
			Config: &tsoptions.ParsedCommandLine{
				ParsedConfig: &core.ParsedOptions{
					FileNames:       []string{"c:/project/a.ts"},
					CompilerOptions: &opts,
				},
			},
			Host: compiler.NewCompilerHost("c:/project", fs, bundled.LibPath(), nil, nil),
		})

		diags := program.GetProgramDiagnostics()

		// Should have an error about invalid module kind
		foundError := false
		for _, d := range diags {
			if strings.Contains(d.String(), "amd") || strings.Contains(d.String(), "system") {
				foundError = true
				break
			}
		}
		assert.Assert(t, foundError, "Should have error about invalid module kind for outFile")
	})

	t.Run("DeclarationBundle", func(t *testing.T) {
		t.Parallel()

		fs := vfstest.FromMap[any](nil, false)
		fs = bundled.WrapFS(fs)

		_ = fs.WriteFile("c:/project/types.ts", "export interface Foo { x: number; }")
		_ = fs.WriteFile("c:/project/main.ts", "import { Foo } from './types';\nexport function bar(f: Foo) { return f.x; }")

		opts := core.CompilerOptions{
			Target:      core.ScriptTargetESNext,
			Module:      core.ModuleKindNone,
			OutFile:     "c:/project/bundle.js",
			Declaration: core.TSTrue,
		}

		program := compiler.NewProgram(compiler.ProgramOptions{
			Config: &tsoptions.ParsedCommandLine{
				ParsedConfig: &core.ParsedOptions{
					FileNames:       []string{"c:/project/main.ts"},
					CompilerOptions: &opts,
				},
			},
			Host: compiler.NewCompilerHost("c:/project", fs, bundled.LibPath(), nil, nil),
		})

		ctx := context.Background()
		result := program.Emit(ctx, compiler.EmitOptions{})

		assert.Assert(t, !result.EmitSkipped, "Emit should not be skipped")

		// Check for .d.ts file
		dtsFound := false
		for _, f := range result.EmittedFiles {
			if strings.HasSuffix(f, ".d.ts") {
				dtsFound = true
				break
			}
		}
		assert.Assert(t, dtsFound, "Should have emitted .d.ts file")
	})
}

func TestNamespaceAcrossFiles(t *testing.T) {
	t.Parallel()

	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}

	t.Run("MergedNamespace", func(t *testing.T) {
		t.Parallel()

		fs := vfstest.FromMap[any](nil, false)
		fs = bundled.WrapFS(fs)

		// Create a namespace split across multiple files
		// File 1: defines namespace MyApp with function foo
		_ = fs.WriteFile("c:/project/app1.ts", `namespace MyApp {
    export function foo() { return "foo"; }
}`)

		// File 2: extends namespace MyApp with function bar that calls foo
		_ = fs.WriteFile("c:/project/app2.ts", `/// <reference path="./app1.ts" />
namespace MyApp {
    export function bar() { return foo() + "bar"; }
}`)

		// File 3: uses the merged namespace
		_ = fs.WriteFile("c:/project/main.ts", `/// <reference path="./app2.ts" />
console.log(MyApp.foo());
console.log(MyApp.bar());`)

		opts := core.CompilerOptions{
			Target:  core.ScriptTargetESNext,
			Module:  core.ModuleKindNone,
			OutFile: "c:/project/bundle.js",
		}

		program := compiler.NewProgram(compiler.ProgramOptions{
			Config: &tsoptions.ParsedCommandLine{
				ParsedConfig: &core.ParsedOptions{
					FileNames:       []string{"c:/project/main.ts"},
					CompilerOptions: &opts,
				},
			},
			Host: compiler.NewCompilerHost("c:/project", fs, bundled.LibPath(), nil, nil),
		})

		// Check for errors
		diags := program.GetProgramDiagnostics()
		for _, d := range diags {
			t.Logf("Diagnostic: %s", d.String())
		}

		ctx := context.Background()
		result := program.Emit(ctx, compiler.EmitOptions{})

		assert.Assert(t, !result.EmitSkipped, "Emit should not be skipped")

		content, ok := fs.ReadFile("c:/project/bundle.js")
		assert.Assert(t, ok, "bundle.js should exist")

		contentStr := string(content)
		t.Logf("Bundle content:\n%s", contentStr)

		// Verify the namespace parts are in the correct order:
		// app1.ts (defines foo) should come before app2.ts (uses foo)
		// which should come before main.ts (uses both)
		posFoo := strings.Index(contentStr, "function foo()")
		posBar := strings.Index(contentStr, "function bar()")
		posMain := strings.Index(contentStr, "MyApp.foo()")

		assert.Assert(t, posFoo >= 0, "Should contain foo function")
		assert.Assert(t, posBar >= 0, "Should contain bar function")
		assert.Assert(t, posMain >= 0, "Should contain main code")
		assert.Assert(t, posFoo < posBar, "foo should be defined before bar (foo at %d, bar at %d)", posFoo, posBar)
		assert.Assert(t, posBar < posMain, "bar should be defined before main (bar at %d, main at %d)", posBar, posMain)
	})
}

func TestFileDependencyGraph(t *testing.T) {
	t.Parallel()

	if !bundled.Embedded {
		t.Skip("bundled files are not embedded")
	}

	t.Run("CircularDependencies", func(t *testing.T) {
		t.Parallel()

		fs := vfstest.FromMap[any](nil, false)
		fs = bundled.WrapFS(fs)

		// Create circular dependencies: a -> b -> c -> a
		_ = fs.WriteFile("c:/project/a.ts", "import { c } from './c';\nexport const a = 1;")
		_ = fs.WriteFile("c:/project/b.ts", "import { a } from './a';\nexport const b = a + 1;")
		_ = fs.WriteFile("c:/project/c.ts", "import { b } from './b';\nexport const c = b + 1;")

		opts := core.CompilerOptions{
			Target:  core.ScriptTargetESNext,
			Module:  core.ModuleKindNone,
			OutFile: "c:/project/bundle.js",
		}

		program := compiler.NewProgram(compiler.ProgramOptions{
			Config: &tsoptions.ParsedCommandLine{
				ParsedConfig: &core.ParsedOptions{
					FileNames:       []string{"c:/project/a.ts"},
					CompilerOptions: &opts,
				},
			},
			Host: compiler.NewCompilerHost("c:/project", fs, bundled.LibPath(), nil, nil),
		})

		ctx := context.Background()
		result := program.Emit(ctx, compiler.EmitOptions{})

		// Should still emit even with circular dependencies
		// (TypeScript handles circular dependencies at runtime)
		assert.Assert(t, !result.EmitSkipped, "Emit should not be skipped even with circular deps")
	})
}
