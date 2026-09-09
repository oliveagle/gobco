package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/types"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

var exit = os.Exit

func main() {
	exit(gobcoMain(os.Stdout, os.Stderr, os.Args...))
}

func gobcoMain(stdout, stderr io.Writer, args ...string) int {
	g := newGobco(stdout, stderr)
	g.parseCommandLine(args)
	g.prepareTmp()
	if g.instrument() {
		g.runGoTest()
		g.printOutput()
	} else {
		_, _ = io.WriteString(g.stdout, "nothing to instrument\n")
	}
	g.cleanUp()
	return g.exitCode
}

type gobco struct {
	branch      bool
	listAll     bool
	immediately bool
	keep        bool
	coverTest   bool

	goTestArgs []string
	args       []argInfo

	statsFilename string

	exitCode int

	logger
	buildEnv
}

func newGobco(stdout io.Writer, stderr io.Writer) *gobco {
	var g gobco
	g.logger.init(stdout, stderr)
	g.buildEnv.init(&g.logger)
	return &g
}

func (g *gobco) parseCommandLine(argv []string) {
	args := g.parseOptions(argv)
	g.parseArgs(args)
}

func (g *gobco) parseOptions(argv []string) []string {
	var help, ver bool

	flags := flag.NewFlagSet(filepath.Base(argv[0]), flag.ContinueOnError)
	flags.BoolVar(&help, "help", false,
		"print the available command line options")
	flags.BoolVar(&g.branch, "branch", false,
		"cover branches, not conditions")
	flags.BoolVar(&g.immediately, "immediately", false,
		"persist the coverage immediately at each check point")
	flags.BoolVar(&g.keep, "keep", false,
		"don't remove the temporary working directory")
	flags.BoolVar(&g.listAll, "list-all", false,
		"at finish, print also those conditions that are fully covered")
	flags.StringVar(&g.statsFilename, "stats", "",
		"load and persist the JSON coverage data to this `file`")
	flags.BoolVar(&g.verbose, "v", false,
		"verbose output")
	flags.BoolVar(&g.verbose, "verbose", false,
		"verbose output (alias of -v)")
	flags.BoolVar(&g.coverTest, "cover-test", false,
		"cover the test code as well")
	flags.BoolVar(&ver, "version", false,
		"print the gobco version")

	flags.SetOutput(g.stderr)
	flags.Usage = func() {
		_, _ = fmt.Fprintf(flags.Output(),
			"usage: %s [options] [packages...] [--] [go test flags...]\n", flags.Name())
		flags.PrintDefaults()
		g.exitCode = 2
	}

	err := flags.Parse(argv[1:])
	if g.exitCode != 0 {
		exit(g.exitCode)
	}
	g.check(err)

	if help {
		flags.SetOutput(g.stdout)
		flags.Usage()
		exit(0)
	}

	if ver {
		g.outf("%s", version)
		exit(0)
	}

	return flags.Args()
}

func (g *gobco) parseArgs(args []string) {
	pkgArgs, goTestFlags := splitPkgsAndGoTestFlags(args)
	g.goTestArgs = goTestFlags

	if len(pkgArgs) == 0 {
		pkgArgs = []string{"."}
	}

	for _, arg := range pkgArgs {
		g.args = append(g.args, g.expandAndClassify(filepath.FromSlash(arg))...)
	}
}

// splitPkgsAndGoTestFlags splits args into packages and go test flags.
// Non-flag args before any flag arg (or before --) are packages.
// Everything after -- is a go test flag.
func splitPkgsAndGoTestFlags(args []string) (pkgs, goTestFlags []string) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:]
		}
	}

	i := 0
	for i < len(args) && !strings.HasPrefix(args[i], "-") {
		i++
	}
	return args[:i], args[i:]
}

func (g *gobco) expandAndClassify(arg string) []argInfo {
	if isRecursivePattern(arg) {
		return g.expandRecursive(arg)
	}
	return []argInfo{g.classifySingle(arg)}
}

func isRecursivePattern(arg string) bool {
	return arg == "..." || strings.HasSuffix(arg, "/...")
}

func (g *gobco) expandRecursive(pattern string) []argInfo {
	baseDir := strings.TrimSuffix(pattern, "/...")
	if baseDir == "" || baseDir == "." {
		baseDir = "."
	}

	moduleRoot, _ := g.findInModule(baseDir)
	if moduleRoot == "" {
		g.check(fmt.Errorf("error: pattern %q must be inside a Go module", pattern))
	}

	absModuleRoot, err := filepath.Abs(moduleRoot)
	g.check(err)

	// Run from the current working directory (not absDir): relative
	// patterns such as "./testdata/..." must resolve the same way
	// they do in classifySingle/findInModule and in a plain `go list`.
	cmd := exec.Command("go", "list", "-find", "-e", "-f", "{{.Dir}}", pattern)
	out, err := cmd.Output()
	if err != nil {
		g.check(fmt.Errorf("error running 'go list %s': %w", pattern, err))
	}

	dirs := strings.Fields(string(out))
	if len(dirs) == 0 {
		g.errf("warning: pattern %q matched no packages", pattern)
		return nil
	}
	seen := make(map[string]bool, len(dirs))
	for i, dir := range dirs {
		if seen[dir] {
			dirs = append(dirs[:i], dirs[i+1:]...)
			i--
		}
		seen[dir] = true
	}

	copyDst := "module-" + randomHex(8)
	var results []argInfo
	for _, dir := range dirs {
		rel, err := filepath.Rel(absModuleRoot, dir)
		g.check(err)

		results = append(results, argInfo{
			arg:       filepath.ToSlash(rel),
			argDir:    dir,
			module:    true,
			copySrc:   absModuleRoot,
			copyDst:   copyDst,
			instrFile: "",
			instrDir:  filepath.Join(copyDst, rel),
		})
	}
	return results
}

// classifySingle determines how to handle the argument, depending on whether it is
// a single file or directory, and whether it is located in a Go module or not.
func (g *gobco) classifySingle(arg string) argInfo {
	st, err := os.Stat(arg)
	isDir := err == nil && st.IsDir()

	dir := arg
	base := ""
	if !isDir {
		dir = filepath.Dir(dir)
		base = filepath.Base(arg)
	}

	if moduleRoot, moduleRel := g.findInModule(dir); moduleRoot != "" {
		copyDst := "module-" + randomHex(8) // Must be outside 'gopath/'.
		packageDir := filepath.Join(copyDst, moduleRel)
		return argInfo{
			arg:       arg,
			argDir:    dir,
			module:    true,
			copySrc:   moduleRoot,
			copyDst:   copyDst,
			instrFile: base,
			instrDir:  packageDir,
		}
	}

	if relDir := g.findInGopath(dir); relDir != "" {
		relDir := filepath.Join("gopath", relDir)
		return argInfo{
			arg:       arg,
			argDir:    dir,
			module:    false,
			copySrc:   dir,
			copyDst:   relDir,
			instrFile: base,
			instrDir:  relDir,
		}
	}

	g.check(fmt.Errorf("error: argument %q must be inside GOPATH", arg))
	panic("unreachable")
}

// findInGopath returns the directory relative to the enclosing GOPATH, if any.
func (g *gobco) findInGopath(arg string) string {
	gopaths := g.gopaths()

	abs, err := filepath.Abs(arg)
	g.check(err)

	for _, gopath := range filepath.SplitList(gopaths) {

		rel, err := filepath.Rel(gopath, abs)
		g.check(err)

		if strings.HasPrefix(rel, "src") {
			return rel
		}
	}
	return ""
}

func (g *gobco) gopaths() string {
	gopaths := os.Getenv("GOPATH")
	if gopaths != "" {
		return gopaths
	}

	home, err := os.UserHomeDir()
	g.check(err)
	return filepath.Join(home, "go")
}

func (g *gobco) findInModule(dir string) (moduleRoot, moduleRel string) {
	absDir, err := filepath.Abs(dir)
	g.check(err)

	abs := absDir
	for {
		if _, err := os.Lstat(filepath.Join(abs, "go.mod")); err == nil {
			rel, err := filepath.Rel(abs, absDir)
			g.check(err)

			root := abs
			if rel == "." {
				root = dir
			}

			return root, rel
		}

		parent := filepath.Dir(abs)
		if parent == abs {
			return "", ""
		}
		abs = parent
	}
}

// prepareTmp copies the source files to the temporary directory.
//
// Later, gobco.instrumenter will overwrite some of these files.
func (g *gobco) prepareTmp() {
	if g.statsFilename != "" {
		var err error
		g.statsFilename, err = filepath.Abs(g.statsFilename)
		g.check(err)
	} else {
		g.statsFilename = g.file("gobco-counts.json")
	}

	copied := make(map[string]bool)
	for _, arg := range g.args {
		dstDir := g.file(arg.copyDst)
		key := arg.copySrc + " -> " + dstDir
		if copied[key] {
			continue
		}
		copied[key] = true
		g.check(copyDir(arg.copySrc, dstDir))
	}
}

func (g *gobco) instrument() bool {
	found := false
	for _, arg := range g.args {
		// A fresh instrumenter per package: the instrumenter
		// accumulates the conditions it finds (i.conds) and writes
		// them into the generated gobco_variable.go, so a shared
		// instance would leak one package's conditions into every
		// other package's stats.
		in := instrumenter{
			g.branch,
			g.coverTest,
			g.immediately,
			g.listAll,
			false,
			nil,
			map[*ast.Package]*types.Package{},
			map[ast.Expr]types.Type{},
			nil,
			0,
			map[ast.Expr]bool{},
			map[ast.Expr]*exprSubst{},
			map[ast.Stmt]*ast.Stmt{},
			map[ast.Stmt]ast.Stmt{},
			false,
			nil,
		}
		instrDst := g.file(arg.instrDir)
		if in.instrument(arg.argDir, arg.instrFile, instrDst) {
			found = true
			g.verbosef("Instrumented %s to %s", arg.arg, instrDst)
		}
	}
	return found
}

func (g *gobco) runGoTest() {
	for _, arg := range g.args {
		gopaths := ""
		if !arg.module {
			gopaths = g.gopaths()
		}
		if rc := (goTest{}).run(
			arg,
			g.goTestArgs,
			g.verbose,
			gopaths,
			g.statsFileFor(arg),
			&g.buildEnv,
		); rc != 0 {
			// First failure wins: with multiple packages a later
			// successful run must not clear an earlier failure.
			g.exitCode = rc
		}
	}
}

// statsFileFor returns the stats file for arg's test run. The
// injected TestMain assumes the file holds exactly the counters
// of its own package (it panics otherwise), so each package gets
// its own file. With a single package this is the plain stats
// file, so -stats accumulation across runs works exactly as
// before. With multiple packages the suffix is the package's
// relative path (sanitized), which is stable across runs, so
// -stats still accumulates per package.
func (g *gobco) statsFileFor(arg argInfo) string {
	if len(g.args) == 1 {
		return g.statsFilename
	}
	suffix := strings.NewReplacer("/", "_", ".", "_", ":", "_").Replace(arg.arg)
	return g.statsFilename + "." + suffix
}

func (g *gobco) printOutput() {
	var files []string
	if len(g.args) == 1 {
		files = []string{g.statsFilename}
	} else {
		for _, arg := range g.args {
			files = append(files, g.statsFileFor(arg))
		}
	}

	var conds []condition
	var lastErr error
	for _, file := range files {
		c, err := g.load(file)
		if err != nil {
			// A missing file means the package's tests never ran
			// (or the run failed before persisting); its conditions
			// are simply absent from the summary.
			lastErr = err
			continue
		}
		conds = append(conds, c...)
	}
	if len(conds) == 0 && lastErr != nil {
		if g.exitCode != 0 {
			return // skip silently
		}
		g.logger.errf("%s", lastErr)
	}

	kind := "Condition coverage"
	if g.branch {
		kind = "Branch coverage"
	}

	totalCov := 0
	for _, c := range conds {
		if c.TrueCount > 0 {
			totalCov++
		}
		if c.FalseCount > 0 {
			totalCov++
		}
	}
	totalCond := len(conds) * 2

	pct := 0.0
	if totalCond > 0 {
		pct = float64(totalCov) / float64(totalCond) * 100
	}

	g.outf("")
	g.outf("%s: %d/%d (%.1f%%)", kind, totalCov, totalCond, pct)

	// Group conditions by file for per-file summary.
	type fileStat struct{ covered, total int }
	byFile := map[string]*fileStat{}
	var fileOrder []string
	for _, c := range conds {
		file := c.Start
		if idx := strings.IndexByte(file, ':'); idx >= 0 {
			file = file[:idx]
		}
		fs, ok := byFile[file]
		if !ok {
			fs = &fileStat{}
			byFile[file] = fs
			fileOrder = append(fileOrder, file)
		}
		fs.total += 2
		if c.TrueCount > 0 {
			fs.covered++
		}
		if c.FalseCount > 0 {
			fs.covered++
		}
	}

	for _, cond := range conds {
		g.printCond(cond)
	}

	g.outf("")
	g.outf("Per-file coverage:")
	for _, file := range fileOrder {
		fs := byFile[file]
		fpct := 0.0
		if fs.total > 0 {
			fpct = float64(fs.covered) / float64(fs.total) * 100
		}
		g.outf("  %s: %d/%d (%.1f%%)", file, fs.covered, fs.total, fpct)
	}
}

func (g *gobco) cleanUp() {
	if g.keep {
		g.errf("")
		g.errf("gobco: the temporary files are in %s", g.tmpdir)
	} else {
		err := os.RemoveAll(g.tmpdir)
		if err != nil {
			g.verbosef("%s", err)
		}
	}
}

func (g *gobco) load(filename string) ([]condition, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}

	defer func() {
		closeErr := file.Close()
		g.check(closeErr)
	}()

	var data []condition
	decoder := json.NewDecoder(bufio.NewReader(file))
	decoder.DisallowUnknownFields()
	g.check(decoder.Decode(&data))

	return data, nil
}

func (g *gobco) printCond(cond condition) {

	trueCount := cond.TrueCount
	falseCount := cond.FalseCount
	if !g.listAll && trueCount > 0 && falseCount > 0 {
		return
	}

	start := cond.Start
	code := cond.Code
	switch {
	case trueCount == 0 && falseCount == 0:
		g.outf("%s: condition %q was never evaluated",
			start, code)
	case trueCount == 0 && falseCount == 1:
		g.outf("%s: condition %q was once false but never true",
			start, code)
	case trueCount == 0:
		g.outf("%s: condition %q was %d times false but never true",
			start, code, falseCount)
	case trueCount == 1 && falseCount == 0:
		g.outf("%s: condition %q was once true but never false",
			start, code)
	case trueCount == 1 && falseCount == 1:
		g.outf("%s: condition %q was once true and once false",
			start, code)
	case trueCount == 1:
		g.outf("%s: condition %q was once true and %d times false",
			start, code, falseCount)
	case falseCount == 0:
		g.outf("%s: condition %q was %d times true but never false",
			start, code, trueCount)
	case falseCount == 1:
		g.outf("%s: condition %q was %d times true and once false",
			start, code, trueCount)
	default:
		g.outf("%s: condition %q was %d times true and %d times false",
			start, code, trueCount, falseCount)
	}
}

// goTest groups the functions that run 'go test' with the proper arguments.
type goTest struct{}

func (t goTest) run(
	arg argInfo,
	extraArgs []string,
	verbose bool,
	gopaths string,
	statsFilename string,
	e *buildEnv,
) int {
	args := t.args(verbose, extraArgs)
	goTest := exec.Command("go", args[1:]...)
	goTest.Stdout = e.stdout
	goTest.Stderr = e.stderr
	goTest.Dir = e.file(arg.instrDir)
	goTest.Env = t.env(e.tmpdir, gopaths, statsFilename)

	cmdline := strings.Join(args, " ")
	e.verbosef("Running %q in %q", cmdline, goTest.Dir)

	err := goTest.Run()
	if err != nil {
		e.errf("go test %s: %s", arg.arg, err)
		return 1
	} else {
		e.verbosef("Finished %s", cmdline)
		return 0
	}
}

func (goTest) args(verbose bool, extraArgs []string) []string {
	args := []string{"go", "test"}

	if verbose {
		// The -v is necessary to produce any output at all.
		// Without it, most of the log output is suppressed.
		args = append(args, "-v")
	}

	// Work around test result caching which does not apply anyway,
	// since the instrumented files are written to a new directory
	// each time.
	//
	// Without this option, 'go test' sometimes needs twice the time.
	args = append(args, "-test.count", "1")

	args = append(args, ".")

	// 'go test' allows flags even after packages.
	args = append(args, extraArgs...)

	return args
}

func (goTest) env(tmpdir, gopaths, statsFilename string) []string {

	var env []string

	for _, envVar := range os.Environ() {
		if gopaths == "" && strings.HasPrefix(envVar, "GOPATH=") {
			continue
		}
		env = append(env, envVar)
	}

	if gopaths != "" {
		gopathDir := filepath.Join(tmpdir, "gopath")
		gopath := gopathDir + string(filepath.ListSeparator) + gopaths
		env = append(env, "GOPATH="+gopath)
		env = append(env, "GO111MODULE=off")
	}

	env = append(env, "GOBCO_STATS="+statsFilename)

	return env
}

// buildEnv describes the environment in which all interesting pieces of code
// are collected and instrumented.
type buildEnv struct {
	tmpdir string
	*logger
}

func (e *buildEnv) init(l *logger) {

	tmpdir := filepath.Join(os.TempDir(), "gobco-"+randomHex(8))

	l.check(os.MkdirAll(tmpdir, 0o777))

	l.verbosef("The temporary working directory is %s", tmpdir)

	*e = buildEnv{tmpdir, l}
}

// file returns the absolute path of the given path, which is interpreted
// relative to the temporary directory.
func (e *buildEnv) file(rel string) string {
	return filepath.Join(e.tmpdir, filepath.FromSlash(rel))
}

// logger provides basic logging and error checking.
type logger struct {
	stdout  io.Writer
	stderr  io.Writer
	verbose bool
}

func (l *logger) init(stdout io.Writer, stderr io.Writer) {
	l.stdout = stdout
	l.stderr = stderr
}

func (l *logger) check(err error) {
	if err != nil {
		l.errf("%s", err)
		exit(1)
	}
}

func (l *logger) outf(format string, args ...interface{}) {
	_, _ = fmt.Fprintf(l.stdout, format+"\n", args...)
}

func (l *logger) errf(format string, args ...interface{}) {
	_, _ = fmt.Fprintf(l.stderr, format+"\n", args...)
}

func (l *logger) verbosef(format string, args ...interface{}) {
	if l.verbose {
		l.errf(format, args...)
	}
}

// argInfo describes the properties of an item that will be instrumented.
//
// If it is inside GOPATH, it or its containing directory is copied, otherwise
// the whole Go module will be copied.
//
// If it is a file, only that file is instrumented, otherwise the whole package
// is instrumented. Even in case of a single file, the whole directory is
// copied though.
type argInfo struct {
	// From the command line, using either '/' or '\\' as separator.
	arg string

	// Either arg if it is a directory, or its containing directory.
	// Either absolute, or relative to the current working directory.
	//
	// This is the directory from which the code is instrumented. The paths
	// to the files in this directory will end up in the coverage output.
	argDir string

	// Whether arg is a module (true) or a traditional package (false).
	module bool

	// The directory that will be copied to the build environment.
	// Either absolute, or relative to the current working directory.
	// For modules, it is the module root, so that go.mod is copied as well.
	// For other packages it is the package directory itself.
	copySrc string

	// The copy destination, relative to tmpdir.
	// For modules, it is some directory outside 'gopath/src',
	// traditional packages are copied to 'gopath/src/$pkgname'.
	copyDst string

	// The single file in which to instrument the code, relative to instrDir,
	// or "" to instrument the whole package.
	instrFile string

	// The directory where the instrumented code is saved, relative to tmpdir.
	// The directory in which to run 'go test', relative to tmpdir.
	instrDir string
}

type condition struct {
	Start      string
	Code       string
	TrueCount  int
	FalseCount int
}
