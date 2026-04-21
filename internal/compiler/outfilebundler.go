package compiler

import (
	"context"
	"strings"
	"sync"

	"github.com/microsoft/typescript-go/internal/ast"
	"github.com/microsoft/typescript-go/internal/core"
	"github.com/microsoft/typescript-go/internal/printer"
	"github.com/microsoft/typescript-go/internal/sourcemap"
	"github.com/microsoft/typescript-go/internal/stringutil"
	"github.com/microsoft/typescript-go/internal/tspath"
)

// shouldEmitBundled returns true when the program should emit a single bundled
// output file (compilerOptions.outFile is set).
func shouldEmitBundled(options *core.CompilerOptions) bool {
	return options.OutFile != ""
}

// EmitBundled performs a bundled emit of all source files into compilerOptions.OutFile.
// Files are ordered by /// <reference path> directives so dependencies come first.
func (p *Program) EmitBundled(ctx context.Context, options EmitOptions) *EmitResult {
	compilerOptions := p.Options()

	sourceFiles := p.getSourceFilesToEmit(options.TargetSourceFile, options.EmitOnly == EmitOnlyForcedDts)
	if len(sourceFiles) == 0 {
		return &EmitResult{}
	}

	b := &outFileBundler{
		program:  p,
		options:  compilerOptions,
		emitOnly: options.EmitOnly,
		writerPool: &sync.Pool{
			New: func() any {
				return printer.NewTextWriter(compilerOptions.NewLine.GetNewLineCharacter(), 0)
			},
		},
	}

	ordered := sortByReferences(p, sourceFiles)
	transformed := make([]*transformedFile, len(ordered))
	for i, sf := range ordered {
		transformed[i] = b.transformFile(ctx, sf)
	}
	return b.writeBundledOutput(ctx, transformed, options)
}

// outFileBundler holds shared state for a single bundled emit.
type outFileBundler struct {
	program     *Program
	options     *core.CompilerOptions
	emitOnly    EmitOnly
	writerPool  *sync.Pool
	diagnostics ast.DiagnosticsCollection
}

// transformedFile holds the JS-transformed form of a source file plus the emit
// context that owns its generated-name mappings (must outlive the printer).
type transformedFile struct {
	sourceFile     *ast.SourceFile
	jsSourceFile   *ast.SourceFile
	jsEmitContext  *printer.EmitContext
	putEmitContext func()
}

// transformFile runs the JS script transformers for a single source file.
func (b *outFileBundler) transformFile(ctx context.Context, sourceFile *ast.SourceFile) *transformedFile {
	tf := &transformedFile{sourceFile: sourceFile}
	if b.emitOnly != EmitAll && b.emitOnly != EmitOnlyJs {
		return tf
	}
	host, done := newEmitHost(ctx, b.program, sourceFile)
	defer done()

	emitContext, putEmitContext := printer.GetEmitContext()
	jsFile := sourceFile
	for _, transformer := range getScriptTransformers(emitContext, host, sourceFile) {
		jsFile = transformer.TransformSourceFile(jsFile)
	}
	tf.jsSourceFile = jsFile
	tf.jsEmitContext = emitContext
	tf.putEmitContext = putEmitContext
	return tf
}

// writeBundledOutput emits the JS bundle (and source map) and the .d.ts bundle.
func (b *outFileBundler) writeBundledOutput(ctx context.Context, files []*transformedFile, options EmitOptions) *EmitResult {
	result := &EmitResult{}
	opts := b.options

	if opts.NoEmit == core.TSTrue {
		result.EmitSkipped = true
		return result
	}

	// writeOne writes content to path via the EmitOptions hook or the host FS,
	// records diagnostics on failure, and tracks emitted files on success.
	writeOne := func(path, content string) {
		var err error
		if options.WriteFile != nil {
			err = options.WriteFile(path, content, &WriteFileData{})
		} else {
			err = b.program.Host().FS().WriteFile(path, content)
		}
		if err != nil {
			b.diagnostics.Add(ast.NewCompilerDiagnostic(nil, path, err.Error()))
			return
		}
		result.EmittedFiles = append(result.EmittedFiles, path)
	}

	emitJS := (b.emitOnly == EmitAll || b.emitOnly == EmitOnlyJs) && opts.EmitDeclarationOnly != core.TSTrue
	if emitJS {
		jsPath := opts.OutFile
		if !tspath.FileExtensionIsOneOf(jsPath, []string{tspath.ExtensionJs, tspath.ExtensionJsx, tspath.ExtensionMjs, tspath.ExtensionCjs}) {
			jsPath = tspath.RemoveFileExtension(jsPath) + tspath.ExtensionJs
		}
		emitMap := opts.SourceMap.IsTrue() && !opts.InlineSourceMap.IsTrue()
		mapPath := ""
		if emitMap {
			mapPath = jsPath + ".map"
		}
		jsContent, mapContent := b.printBundledJS(files, jsPath, mapPath)
		if emitMap && mapContent != "" {
			writeOne(mapPath, mapContent)
		}
		if opts.EmitBOM.IsTrue() {
			jsContent = stringutil.AddUTF8ByteOrderMark(jsContent)
		}
		writeOne(jsPath, jsContent)
	}

	emitDts := (b.emitOnly == EmitAll || b.emitOnly == EmitOnlyDts || b.emitOnly == EmitOnlyForcedDts) && opts.GetEmitDeclarations()
	if emitDts {
		dtsPath := getDtsOutFilePath(opts.OutFile)
		dtsContent := b.printBundledDts(ctx, files)
		if opts.EmitBOM.IsTrue() {
			dtsContent = stringutil.AddUTF8ByteOrderMark(dtsContent)
		}
		writeOne(dtsPath, dtsContent)
	}

	result.Diagnostics = b.diagnostics.GetDiagnostics()
	return result
}

// printBundledJS prints all transformed files into one buffer, joined by the
// configured newline. Each file is printed through its own per-file source map
// generator (the shared generator cannot backtrack to line 0 when a new file's
// printer starts), then the per-file mappings are replayed into the bundled
// generator with a line-offset shift so the resulting source map correctly
// maps bundled positions back to the original .ts files.
func (b *outFileBundler) printBundledJS(files []*transformedFile, jsPath, mapPath string) (string, string) {
	emitMap := b.options.SourceMap.IsTrue() || b.options.InlineSourceMap.IsTrue()
	printerOptions := printer.PrinterOptions{
		RemoveComments:  b.options.RemoveComments.IsTrue(),
		NewLine:         b.options.NewLine,
		NoEmitHelpers:   b.options.NoEmitHelpers.IsTrue(),
		SourceMap:       emitMap,
		InlineSourceMap: b.options.InlineSourceMap.IsTrue(),
		InlineSources:   b.options.InlineSources.IsTrue(),
		Target:          b.options.Target,
	}
	cmpOpts := tspath.ComparePathsOptions{
		UseCaseSensitiveFileNames: b.program.UseCaseSensitiveFileNames(),
		CurrentDirectory:          b.program.GetCurrentDirectory(),
	}
	mapName := tspath.GetBaseFileName(tspath.NormalizeSlashes(jsPath))
	mapDir := tspath.GetDirectoryPath(tspath.NormalizePath(jsPath))
	sourceRoot := getSourceRoot(b.options)
	newGen := func() *sourcemap.Generator {
		if !emitMap {
			return nil
		}
		return sourcemap.NewGenerator(mapName, sourceRoot, mapDir, cmpOpts)
	}
	mapGen := newGen()

	newLine := b.options.NewLine.GetNewLineCharacter()
	var out strings.Builder
	var lineOffset int // Bundle line where the next emitted character will land.
	for _, tf := range files {
		if tf.jsSourceFile == nil {
			continue
		}
		emitContext := tf.jsEmitContext
		if emitContext == nil {
			var put func()
			emitContext, put = printer.GetEmitContext()
			defer put()
		}

		writer := b.writerPool.Get().(printer.EmitTextWriter)
		writer.Clear()
		fileGen := newGen()
		printer.NewPrinter(printerOptions, printer.PrintHandlers{}, emitContext).
			Write(tf.jsSourceFile.AsNode(), tf.sourceFile, writer, fileGen)

		if out.Len() > 0 {
			out.WriteString(newLine)
			lineOffset++
		}
		fileStartLine := lineOffset
		content := writer.String()
		out.WriteString(content)
		lineOffset += strings.Count(content, "\n")
		b.writerPool.Put(writer)

		if mapGen != nil && fileGen != nil {
			b.replayMappings(mapGen, fileGen, fileStartLine)
		}
		if tf.putEmitContext != nil {
			tf.putEmitContext()
		}
	}

	jsContent := out.String()
	if mapGen == nil {
		return jsContent, ""
	}
	if mapPath != "" && !b.options.InlineSourceMap.IsTrue() {
		jsContent += newLine + "//# sourceMappingURL=" + tspath.GetBaseFileName(mapPath)
	}
	return jsContent, mapGen.String()
}

// replayMappings copies fileGen's mappings into mapGen, shifting generated
// line numbers by lineOffset and translating per-file source/name indices to
// mapGen's index space. fileGen.Sources() returns absolute paths (rawSources),
// not the relativized ones in RawSourceMap().Sources — the latter would be
// double-relativized when fed back through AddSource. AddSource and AddName
// are idempotent on path/name, so shared sources across files collapse to the
// same index.
func (b *outFileBundler) replayMappings(mapGen, fileGen *sourcemap.Generator, lineOffset int) {
	rawMap := fileGen.RawSourceMap()
	rawSources := fileGen.Sources()
	srcXlate := make([]sourcemap.SourceIndex, len(rawSources))
	for i, s := range rawSources {
		srcXlate[i] = mapGen.AddSource(s)
	}
	nameXlate := make([]sourcemap.NameIndex, len(rawMap.Names))
	for i, n := range rawMap.Names {
		nameXlate[i] = mapGen.AddName(n)
	}
	for m := range sourcemap.DecodeMappings(rawMap.Mappings).Values() {
		gline := m.GeneratedLine + lineOffset
		switch {
		case !m.IsSourceMapping():
			_ = mapGen.AddGeneratedMapping(gline, m.GeneratedCharacter)
		case m.NameIndex == sourcemap.MissingName:
			_ = mapGen.AddSourceMapping(gline, m.GeneratedCharacter, srcXlate[m.SourceIndex], m.SourceLine, m.SourceCharacter)
		default:
			_ = mapGen.AddNamedSourceMapping(gline, m.GeneratedCharacter, srcXlate[m.SourceIndex], m.SourceLine, m.SourceCharacter, nameXlate[m.NameIndex])
		}
	}
}

// printBundledDts prints all source files' .d.ts forms into one buffer.
func (b *outFileBundler) printBundledDts(ctx context.Context, files []*transformedFile) string {
	printerOptions := printer.PrinterOptions{
		RemoveComments:      b.options.RemoveComments.IsTrue(),
		OnlyPrintJSDocStyle: true,
		NewLine:             b.options.NewLine,
		NoEmitHelpers:       b.options.NoEmitHelpers.IsTrue(),
	}
	emitContext, putEmitContext := printer.GetEmitContext()
	defer putEmitContext()
	prn := printer.NewPrinter(printerOptions, printer.PrintHandlers{}, emitContext)

	newLine := b.options.NewLine.GetNewLineCharacter()
	var out strings.Builder
	for _, tf := range files {
		if tf.sourceFile == nil {
			continue
		}
		host, done := newEmitHost(ctx, b.program, tf.sourceFile)
		dtsFile := tf.sourceFile
		for _, transformer := range (&emitter{host: host}).getDeclarationTransformers(emitContext, "", "") {
			dtsFile = transformer.TransformSourceFile(dtsFile)
		}
		done()

		writer := b.writerPool.Get().(printer.EmitTextWriter)
		writer.Clear()
		prn.Write(dtsFile.AsNode(), tf.sourceFile, writer, nil)

		if out.Len() > 0 {
			out.WriteString(newLine)
		}
		out.WriteString(writer.String())
		b.writerPool.Put(writer)
	}
	return out.String()
}

// getDtsOutFilePath returns the .d.ts output path for the given outFile.
func getDtsOutFilePath(outFile string) string {
	if tspath.FileExtensionIs(outFile, tspath.ExtensionDts) {
		return outFile
	}
	return tspath.RemoveFileExtension(outFile) + tspath.ExtensionDts
}

// sortByReferences orders files so that each file's /// <reference path>
// dependencies appear before it. Walks files in input order, recursively
// emitting each file's references first (DFS post-order). Cycles are broken by
// emitting the offending file once, in the order first encountered.
func sortByReferences(p *Program, files []*ast.SourceFile) []*ast.SourceFile {
	byPath := make(map[tspath.Path]*ast.SourceFile, len(files))
	byBase := make(map[string]*ast.SourceFile, len(files))
	for _, sf := range files {
		byPath[sf.Path()] = sf
		byBase[tspath.GetBaseFileName(sf.FileName())] = sf
	}

	resolveRef := func(sf *ast.SourceFile, ref *ast.FileReference) *ast.SourceFile {
		fullPath := tspath.CombinePaths(tspath.GetDirectoryPath(sf.FileName()), ref.FileName)
		normalized := tspath.ToPath(fullPath, p.GetCurrentDirectory(), p.UseCaseSensitiveFileNames())
		if dep, ok := byPath[normalized]; ok {
			return dep
		}
		return byBase[tspath.GetBaseFileName(ref.FileName)]
	}

	visited := make(map[tspath.Path]bool, len(files))
	onStack := make(map[tspath.Path]bool, len(files))
	result := make([]*ast.SourceFile, 0, len(files))

	var visit func(*ast.SourceFile)
	visit = func(sf *ast.SourceFile) {
		path := sf.Path()
		if visited[path] || onStack[path] {
			return
		}
		onStack[path] = true
		for _, ref := range sf.ReferencedFiles {
			if ref == nil {
				continue
			}
			if dep := resolveRef(sf, ref); dep != nil && dep.Path() != path {
				visit(dep)
			}
		}
		onStack[path] = false
		visited[path] = true
		result = append(result, sf)
	}
	for _, sf := range files {
		visit(sf)
	}
	return result
}
