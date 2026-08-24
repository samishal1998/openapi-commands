// Package gen emits strongly-typed Go source from an OpenAPI spec: struct
// types for component schemas and one Cobra command constructor per
// operation. Generated code is gofmt-formatted, carries a DO NOT EDIT
// header, and is compile-time checked by the Go compiler.
//
// The cmd/oascmd-gen CLI wraps this package for go:generate use.
package gen

import (
	"bytes"
	"fmt"
	"go/format"
	"strconv"
	"strings"

	"github.com/samishal1998/openapi-commands"
	"github.com/samishal1998/openapi-commands/lock"
	"github.com/samishal1998/openapi-commands/spec"
)

// CommandModel is the emit-time view of one generated command that hooks may
// mutate before code is written.
type CommandModel struct {
	// FuncName is the constructor name, e.g. NewDnsRecordsGetCommand.
	FuncName string
	// Path is the command path in the tree.
	Path oascmd.CommandPath
	Op   oascmd.Operation
}

// Options configures generation.
type Options struct {
	// PackageName of the emitted file. Required.
	PackageName string
	// NameFunc overrides the command-path derivation;
	// oascmd.DeriveCommandPath when nil.
	NameFunc oascmd.NameFunc
	// Hooks: OnReadOperation is honored (filter/transform operations,
	// return oascmd.SkipOperation to drop one). The command-creation
	// hooks cannot run at generation time; use OnEmitOperation instead.
	Hooks oascmd.Hooks
	// OnEmitOperation runs per operation before its constructor is
	// emitted. It may mutate cmd (rename the constructor, adjust the
	// command path) or veto it by returning oascmd.SkipOperation.
	OnEmitOperation func(cmd *CommandModel) error
}

// Generate parses an OpenAPI 3.0/3.1 document and returns a gofmt-formatted
// Go source file containing component structs and command constructors.
func Generate(specData []byte, opts Options) ([]byte, error) {
	source, _, err := GenerateWithModels(specData, opts)
	return source, err
}

// GenerateWithModels is Generate plus the post-hook command models it
// emitted, in emission order. The models are what the lock file records:
// they reflect every OnReadOperation and OnEmitOperation mutation, so they
// describe what was actually generated rather than what the spec said.
func GenerateWithModels(specData []byte, opts Options) ([]byte, []CommandModel, error) {
	if opts.PackageName == "" {
		return nil, nil, fmt.Errorf("gen: package name is required")
	}
	ops, schemas, err := spec.LoadWithSchemas(specData)
	if err != nil {
		return nil, nil, err
	}
	nameFunc := opts.NameFunc
	if nameFunc == nil {
		nameFunc = oascmd.DeriveCommandPath
	}

	var models []CommandModel
	usedNames := map[string]int{}
	for i := range ops {
		op := ops[i]
		keep, err := opts.Hooks.ApplyRead(&op)
		if err != nil {
			return nil, nil, fmt.Errorf("%s %s: %w", op.Method, op.Path, err)
		}
		if !keep {
			continue
		}
		if err := opts.Hooks.ApplyBody(&op); err != nil {
			return nil, nil, fmt.Errorf("%s %s: %w", op.Method, op.Path, err)
		}
		path := nameFunc(op)
		model := CommandModel{
			FuncName: "New" + pascalWords(append(append([]string{}, path.Groups...), path.Name)) + "Command",
			Path:     path,
			Op:       op,
		}
		if opts.OnEmitOperation != nil {
			if err := opts.OnEmitOperation(&model); err != nil {
				if err == oascmd.SkipOperation {
					continue
				}
				return nil, nil, fmt.Errorf("%s %s: %w", op.Method, op.Path, err)
			}
		}
		usedNames[model.FuncName]++
		if n := usedNames[model.FuncName]; n > 1 {
			model.FuncName = fmt.Sprintf("%s%d", model.FuncName, n)
		}
		models = append(models, model)
	}

	var buf bytes.Buffer
	emitFile(&buf, opts.PackageName, schemas, models)
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, nil, fmt.Errorf("gen: format emitted source: %w\n%s", err, buf.String())
	}
	return formatted, models, nil
}

// GenerateFromModels emits source for an explicit set of command models,
// taking only the component schemas from the spec. It backs the
// additive-only drift policy, which replaces some freshly derived models
// with the ones recorded in the lock file.
func GenerateFromModels(specData []byte, opts Options, models []CommandModel) ([]byte, error) {
	if opts.PackageName == "" {
		return nil, fmt.Errorf("gen: package name is required")
	}
	_, schemas, err := spec.LoadWithSchemas(specData)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	emitFile(&buf, opts.PackageName, schemas, models)
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("gen: format emitted source: %w\n%s", err, buf.String())
	}
	return formatted, nil
}

// ModelFromLock converts a lock model back into a command model, so a
// previously generated operation can be emitted unchanged.
func ModelFromLock(m lock.Model) CommandModel {
	return CommandModel{FuncName: m.FuncName, Path: m.Path, Op: m.Op}
}

// LockModels converts emitted command models to the lock package's model
// type. It is a small adapter so gen does not depend on lock.
func LockModels(models []CommandModel) []lock.Model {
	out := make([]lock.Model, 0, len(models))
	for _, m := range models {
		out = append(out, lock.Model{FuncName: m.FuncName, Path: m.Path, Op: m.Op})
	}
	return out
}

func emitFile(buf *bytes.Buffer, pkg string, schemas []spec.ComponentSchema, models []CommandModel) {
	fmt.Fprintf(buf, "// Code generated by oascmd-gen. DO NOT EDIT.\n\n")
	fmt.Fprintf(buf, "package %s\n\n", pkg)

	imports := collectImports(schemas, models)
	if len(imports) > 0 {
		fmt.Fprintf(buf, "import (\n")
		for _, imp := range imports {
			fmt.Fprintf(buf, "\t%s\n", imp)
		}
		fmt.Fprintf(buf, ")\n\n")
	}

	for _, s := range schemas {
		emitSchema(buf, s)
	}
	if len(models) > 0 {
		emitTree(buf, models)
	}
	for i := range models {
		emitCommand(buf, &models[i])
	}
}

func collectImports(schemas []spec.ComponentSchema, models []CommandModel) []string {
	std := map[string]bool{}
	needsOascmd := len(models) > 0
	needsCobra := len(models) > 0
	for _, s := range schemas {
		for _, f := range s.Fields {
			if f.Raw {
				std["encoding/json"] = true
			}
		}
	}
	for i := range models {
		op := &models[i].Op
		if len(models) > 0 {
			std["os"] = true
		}
		for _, p := range op.Params {
			if p.In == "query" {
				std["net/url"] = true
			}
			if !p.Type.Array && p.In != "" {
				switch p.Type.Kind {
				case oascmd.KindInt, oascmd.KindNumber, oascmd.KindBool:
					std["strconv"] = true
				}
			}
			if p.Type.Array && p.In == "query" {
				switch p.Type.Kind {
				case oascmd.KindInt, oascmd.KindNumber, oascmd.KindBool:
					std["strconv"] = true
				}
			}
		}
		if op.Ext.Confirm {
			std["fmt"] = true
		}
	}

	var lines []string
	for _, imp := range sortedKeys(std) {
		lines = append(lines, strconv.Quote(imp))
	}
	if len(lines) > 0 && (needsCobra || needsOascmd) {
		lines = append(lines, "")
	}
	if needsCobra {
		lines = append(lines, strconv.Quote("github.com/spf13/cobra"))
		lines = append(lines, "")
	}
	if needsOascmd {
		lines = append(lines, strconv.Quote("github.com/samishal1998/openapi-commands"))
	}
	return lines
}

func sortedKeys(m map[string]bool) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func emitSchema(buf *bytes.Buffer, s spec.ComponentSchema) {
	if s.Description != "" {
		emitDocComment(buf, fmt.Sprintf("%s: %s", pascal(s.Name), s.Description))
	} else {
		emitDocComment(buf, fmt.Sprintf("%s is the %q component schema.", pascal(s.Name), s.Name))
	}
	fmt.Fprintf(buf, "type %s struct {\n", pascal(s.Name))
	for _, f := range s.Fields {
		tag := f.Name
		if !f.Required {
			tag += ",omitempty"
		}
		fmt.Fprintf(buf, "\t%s %s `json:%s`\n", pascal(f.Name), goType(f), strconv.Quote(tag))
	}
	fmt.Fprintf(buf, "}\n\n")
}

func goType(f spec.ComponentField) string {
	if f.Raw {
		return "json.RawMessage"
	}
	if f.Ref != "" {
		if f.Type.Array {
			return "[]" + pascal(f.Ref)
		}
		return "*" + pascal(f.Ref)
	}
	base := scalarGoType(f.Type.Kind)
	if f.Type.Array {
		return "[]" + base
	}
	return base
}

func scalarGoType(k oascmd.Kind) string {
	switch k {
	case oascmd.KindInt:
		return "int64"
	case oascmd.KindNumber:
		return "float64"
	case oascmd.KindBool:
		return "bool"
	default:
		return "string"
	}
}

// emitTree emits NewCommandTree, which builds group commands and attaches
// every generated command.
func emitTree(buf *bytes.Buffer, models []CommandModel) {
	emitDocComment(buf, "NewCommandTree returns the full generated command tree: group commands with every operation command attached. Attach the result onto your root command, or use the per-operation constructors directly.")
	fmt.Fprintf(buf, "func NewCommandTree(exec oascmd.ExecOptions) []*cobra.Command {\n")
	fmt.Fprintf(buf, "\troot := &cobra.Command{}\n")
	fmt.Fprintf(buf, "\tgroups := map[string]*cobra.Command{\"\": root}\n")
	fmt.Fprintf(buf, "\tgroup := func(path ...string) *cobra.Command {\n")
	fmt.Fprintf(buf, "\t\tkey := \"\"\n")
	fmt.Fprintf(buf, "\t\tcmd := root\n")
	fmt.Fprintf(buf, "\t\tfor _, word := range path {\n")
	fmt.Fprintf(buf, "\t\t\tkey += \"/\" + word\n")
	fmt.Fprintf(buf, "\t\t\tchild, ok := groups[key]\n")
	fmt.Fprintf(buf, "\t\t\tif !ok {\n")
	fmt.Fprintf(buf, "\t\t\t\tchild = &cobra.Command{Use: word}\n")
	fmt.Fprintf(buf, "\t\t\t\tcmd.AddCommand(child)\n")
	fmt.Fprintf(buf, "\t\t\t\tgroups[key] = child\n")
	fmt.Fprintf(buf, "\t\t\t}\n")
	fmt.Fprintf(buf, "\t\t\tcmd = child\n")
	fmt.Fprintf(buf, "\t\t}\n")
	fmt.Fprintf(buf, "\t\treturn cmd\n")
	fmt.Fprintf(buf, "\t}\n")
	for i := range models {
		m := &models[i]
		args := make([]string, len(m.Path.Groups))
		for j, g := range m.Path.Groups {
			args[j] = strconv.Quote(g)
		}
		fmt.Fprintf(buf, "\tgroup(%s).AddCommand(%s(exec))\n", strings.Join(args, ", "), m.FuncName)
	}
	fmt.Fprintf(buf, "\treturn root.Commands()\n")
	fmt.Fprintf(buf, "}\n\n")
}

func emitCommand(buf *bytes.Buffer, m *CommandModel) {
	op := &m.Op
	emitDocComment(buf, fmt.Sprintf("%s returns the %q command (%s %s).",
		m.FuncName, strings.Join(append(append([]string{}, m.Path.Groups...), m.Path.Name), " "), op.Method, op.Path))

	fmt.Fprintf(buf, "func %s(exec oascmd.ExecOptions) *cobra.Command {\n", m.FuncName)

	type boundFlag struct {
		varName string
		flag    string
		p       oascmd.Param
	}
	type boundProp struct {
		varName string
		flag    string
		prop    oascmd.BodyProp
	}
	var params []boundFlag
	var props []boundProp

	// Flag variable declarations.
	for _, p := range op.Params {
		flag := oascmd.FlagName(p.Name, p.Ext)
		params = append(params, boundFlag{varName: "flag" + pascal(flag), flag: flag, p: p})
	}
	if op.Body != nil && op.Body.Flat {
		for _, prop := range op.Body.Props {
			flag := oascmd.FlagName(prop.Name, prop.Ext)
			props = append(props, boundProp{varName: "body" + pascal(flag), flag: flag, prop: prop})
		}
	}
	if len(params) > 0 || len(props) > 0 || op.Body != nil {
		fmt.Fprintf(buf, "\tvar (\n")
		for _, b := range params {
			fmt.Fprintf(buf, "\t\t%s %s\n", b.varName, flagGoType(b.p.Type))
		}
		for _, b := range props {
			fmt.Fprintf(buf, "\t\t%s %s\n", b.varName, flagGoType(b.prop.Type))
		}
		if op.Body != nil {
			fmt.Fprintf(buf, "\t\tflagData string\n")
		}
		fmt.Fprintf(buf, "\t)\n")
	}

	short := op.Summary
	if short == "" {
		short = fmt.Sprintf("%s %s", op.Method, op.Path)
	}
	fmt.Fprintf(buf, "\tcmd := &cobra.Command{\n")
	fmt.Fprintf(buf, "\t\tUse: %s,\n", strconv.Quote(m.Path.Name))
	fmt.Fprintf(buf, "\t\tShort: %s,\n", strconv.Quote(short))
	if op.Description != "" {
		fmt.Fprintf(buf, "\t\tLong: %s,\n", strconv.Quote(op.Description))
	}
	if op.Ext.Hidden {
		fmt.Fprintf(buf, "\t\tHidden: true,\n")
	}
	if op.Deprecated {
		fmt.Fprintf(buf, "\t\tDeprecated: %s,\n", strconv.Quote("this operation is deprecated"))
	}
	fmt.Fprintf(buf, "\t}\n")

	// Flag registration.
	for _, b := range params {
		emitFlagRegistration(buf, b.varName, b.flag, b.p.Ext.Shorthand, b.p.Type, b.p.Default, flagUsage(b.p.Description, b.p.Enum))
	}
	for _, b := range props {
		emitFlagRegistration(buf, b.varName, b.flag, b.prop.Ext.Shorthand, b.prop.Type, b.prop.Default, flagUsage(b.prop.Description, b.prop.Enum))
	}
	if op.Body != nil {
		fmt.Fprintf(buf, "\tcmd.Flags().StringVar(&flagData, \"data\", \"\", \"request body as raw JSON (wins over per-property flags)\")\n")
	}
	fmt.Fprintf(buf, "\tcmd.Flags().Bool(\"json\", false, \"print the raw JSON response\")\n")
	if op.Ext.Confirm {
		fmt.Fprintf(buf, "\tcmd.Flags().Bool(\"yes\", false, \"skip the confirmation prompt\")\n")
	}
	for _, b := range params {
		if b.p.Required && b.p.Default == "" {
			fmt.Fprintf(buf, "\t_ = cmd.MarkFlagRequired(%s)\n", strconv.Quote(b.flag))
		}
	}

	// RunE.
	fmt.Fprintf(buf, "\tcmd.RunE = func(cmd *cobra.Command, args []string) error {\n")
	for _, b := range params {
		emitEnumCheck(buf, b.flag, b.varName, b.p.Type, b.p.Enum)
	}
	for _, b := range props {
		emitEnumCheck(buf, b.flag, b.varName, b.prop.Type, b.prop.Enum)
	}
	if op.Ext.Confirm {
		fmt.Fprintf(buf, "\t\tif yes, _ := cmd.Flags().GetBool(\"yes\"); !yes {\n")
		fmt.Fprintf(buf, "\t\t\tok, err := oascmd.Confirm(cmd.InOrStdin(), cmd.ErrOrStderr(), %s)\n",
			strconv.Quote(fmt.Sprintf("About to run %s %s. Continue?", op.Method, op.Path)))
		fmt.Fprintf(buf, "\t\t\tif err != nil {\n\t\t\t\treturn err\n\t\t\t}\n")
		fmt.Fprintf(buf, "\t\t\tif !ok {\n\t\t\t\treturn fmt.Errorf(\"aborted\")\n\t\t\t}\n")
		fmt.Fprintf(buf, "\t\t}\n")
	}

	fmt.Fprintf(buf, "\t\treq := oascmd.Request{\n")
	fmt.Fprintf(buf, "\t\t\tMethod: %s,\n", strconv.Quote(op.Method))
	fmt.Fprintf(buf, "\t\t\tPath: %s,\n", strconv.Quote(op.Path))
	hasPath, hasQuery := false, false
	for _, b := range params {
		if b.p.In == "path" {
			hasPath = true
		} else {
			hasQuery = true
		}
	}
	if hasPath {
		fmt.Fprintf(buf, "\t\t\tPathParams: map[string]string{},\n")
	}
	if hasQuery {
		fmt.Fprintf(buf, "\t\t\tQuery: url.Values{},\n")
	}
	fmt.Fprintf(buf, "\t\t}\n")

	for _, b := range params {
		if b.p.In == "path" {
			fmt.Fprintf(buf, "\t\treq.PathParams[%s] = %s\n", strconv.Quote(b.p.Name), toStringExpr(b.varName, b.p.Type.Kind))
			continue
		}
		// Params with a default or marked required are always sent;
		// optional ones only when the user set the flag.
		always := b.p.Default != "" || b.p.Required
		guard := fmt.Sprintf("cmd.Flags().Changed(%s)", strconv.Quote(b.flag))
		if b.p.Type.Array {
			if always {
				fmt.Fprintf(buf, "\t\tfor _, v := range %s {\n", b.varName)
				fmt.Fprintf(buf, "\t\t\treq.Query.Add(%s, %s)\n", strconv.Quote(b.p.Name), toStringExpr("v", b.p.Type.Kind))
				fmt.Fprintf(buf, "\t\t}\n")
			} else {
				fmt.Fprintf(buf, "\t\tif %s {\n", guard)
				fmt.Fprintf(buf, "\t\t\tfor _, v := range %s {\n", b.varName)
				fmt.Fprintf(buf, "\t\t\t\treq.Query.Add(%s, %s)\n", strconv.Quote(b.p.Name), toStringExpr("v", b.p.Type.Kind))
				fmt.Fprintf(buf, "\t\t\t}\n")
				fmt.Fprintf(buf, "\t\t}\n")
			}
		} else {
			if always {
				fmt.Fprintf(buf, "\t\treq.Query.Set(%s, %s)\n", strconv.Quote(b.p.Name), toStringExpr(b.varName, b.p.Type.Kind))
			} else {
				fmt.Fprintf(buf, "\t\tif %s {\n", guard)
				fmt.Fprintf(buf, "\t\t\treq.Query.Set(%s, %s)\n", strconv.Quote(b.p.Name), toStringExpr(b.varName, b.p.Type.Kind))
				fmt.Fprintf(buf, "\t\t}\n")
			}
		}
	}

	if op.Body != nil {
		fmt.Fprintf(buf, "\t\tbody := map[string]any{}\n")
		for _, b := range props {
			fmt.Fprintf(buf, "\t\tif cmd.Flags().Changed(%s) {\n", strconv.Quote(b.flag))
			fmt.Fprintf(buf, "\t\t\tbody[%s] = %s\n", strconv.Quote(b.prop.Name), b.varName)
			fmt.Fprintf(buf, "\t\t}\n")
		}
		fmt.Fprintf(buf, "\t\tswitch {\n")
		fmt.Fprintf(buf, "\t\tcase flagData != \"\":\n")
		fmt.Fprintf(buf, "\t\t\treq.RawBody = []byte(flagData)\n")
		fmt.Fprintf(buf, "\t\tcase len(body) > 0:\n")
		if len(op.Body.WrapPath) > 0 {
			quoted := make([]string, len(op.Body.WrapPath))
			for i, seg := range op.Body.WrapPath {
				quoted[i] = strconv.Quote(seg)
			}
			fmt.Fprintf(buf, "\t\t\treq.Body = oascmd.NestBody([]string{%s}, body)\n", strings.Join(quoted, ", "))
		} else {
			fmt.Fprintf(buf, "\t\t\treq.Body = body\n")
		}
		fmt.Fprintf(buf, "\t\t}\n")
	}

	fmt.Fprintf(buf, "\t\te := exec\n")
	fmt.Fprintf(buf, "\t\traw, _ := cmd.Flags().GetBool(\"json\")\n")
	fmt.Fprintf(buf, "\t\te.Raw = e.Raw || raw\n")
	fmt.Fprintf(buf, "\t\tif e.Out == nil {\n\t\t\te.Out = os.Stdout\n\t\t}\n")
	fmt.Fprintf(buf, "\t\treturn oascmd.Execute(cmd.Context(), e, req)\n")
	fmt.Fprintf(buf, "\t}\n")
	fmt.Fprintf(buf, "\treturn cmd\n")
	fmt.Fprintf(buf, "}\n\n")
}

func emitFlagRegistration(buf *bytes.Buffer, varName, flag, shorthand string, typ oascmd.Type, def, usage string) {
	method, defExpr := flagVarMethod(typ, def)
	if shorthand != "" {
		fmt.Fprintf(buf, "\tcmd.Flags().%sP(&%s, %s, %s, %s, %s)\n",
			method, varName, strconv.Quote(flag), strconv.Quote(shorthand), defExpr, strconv.Quote(usage))
	} else {
		fmt.Fprintf(buf, "\tcmd.Flags().%s(&%s, %s, %s, %s)\n",
			method, varName, strconv.Quote(flag), defExpr, strconv.Quote(usage))
	}
}

// flagVarMethod returns the pflag *Var method name and the default-value
// expression for a flag of the given type.
func flagVarMethod(typ oascmd.Type, def string) (string, string) {
	if typ.Array {
		switch typ.Kind {
		case oascmd.KindInt:
			return "Int64SliceVar", "nil"
		case oascmd.KindNumber:
			return "Float64SliceVar", "nil"
		case oascmd.KindBool:
			return "BoolSliceVar", "nil"
		default:
			return "StringSliceVar", "nil"
		}
	}
	switch typ.Kind {
	case oascmd.KindInt:
		if def == "" {
			def = "0"
		}
		return "Int64Var", def
	case oascmd.KindNumber:
		if def == "" {
			def = "0"
		}
		return "Float64Var", def
	case oascmd.KindBool:
		if def == "" {
			def = "false"
		}
		return "BoolVar", def
	default:
		return "StringVar", strconv.Quote(def)
	}
}

func flagGoType(typ oascmd.Type) string {
	base := scalarGoType(typ.Kind)
	if typ.Array {
		return "[]" + base
	}
	return base
}

func emitEnumCheck(buf *bytes.Buffer, flag, varName string, typ oascmd.Type, enum []string) {
	if len(enum) == 0 {
		return
	}
	quoted := make([]string, len(enum))
	for i, e := range enum {
		quoted[i] = strconv.Quote(e)
	}
	allowed := "[]string{" + strings.Join(quoted, ", ") + "}"
	valueExpr := varName
	if typ.Array {
		valueExpr = varName + "..."
	}
	fmt.Fprintf(buf, "\t\tif cmd.Flags().Changed(%s) {\n", strconv.Quote(flag))
	fmt.Fprintf(buf, "\t\t\tif err := oascmd.ValidateEnum(%s, %s, %s); err != nil {\n",
		strconv.Quote(flag), allowed, valueExpr)
	fmt.Fprintf(buf, "\t\t\t\treturn err\n")
	fmt.Fprintf(buf, "\t\t\t}\n")
	fmt.Fprintf(buf, "\t\t}\n")
}

// toStringExpr renders expr (a variable of the given scalar kind) as a
// string-typed Go expression.
func toStringExpr(expr string, kind oascmd.Kind) string {
	switch kind {
	case oascmd.KindInt:
		return fmt.Sprintf("strconv.FormatInt(%s, 10)", expr)
	case oascmd.KindNumber:
		return fmt.Sprintf("strconv.FormatFloat(%s, 'f', -1, 64)", expr)
	case oascmd.KindBool:
		return fmt.Sprintf("strconv.FormatBool(%s)", expr)
	default:
		return expr
	}
}

func flagUsage(description string, enum []string) string {
	usage := description
	if len(enum) > 0 {
		usage = strings.TrimSpace(usage + " (one of: " + strings.Join(enum, ", ") + ")")
	}
	return usage
}

func emitDocComment(buf *bytes.Buffer, text string) {
	const width = 74
	words := strings.Fields(text)
	line := "//"
	for _, w := range words {
		if len(line)+1+len(w) > width && line != "//" {
			fmt.Fprintf(buf, "%s\n", line)
			line = "//"
		}
		line += " " + w
	}
	if line != "//" {
		fmt.Fprintf(buf, "%s\n", line)
	}
}

func pascalWords(words []string) string {
	var b strings.Builder
	for _, w := range words {
		b.WriteString(pascal(w))
	}
	return b.String()
}

// pascal converts a kebab/snake/camel name to PascalCase, upper-casing known
// initialisms (ID, URL, API, DNS, IP, HTTP, JSON).
func pascal(s string) string {
	initialisms := map[string]string{
		"id": "ID", "url": "URL", "api": "API", "dns": "DNS",
		"ip": "IP", "http": "HTTP", "json": "JSON", "ttl": "TTL",
	}
	var b strings.Builder
	for _, w := range splitIdentWords(s) {
		if up, ok := initialisms[w]; ok {
			b.WriteString(up)
			continue
		}
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

func splitIdentWords(s string) []string {
	var words []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	runes := []rune(s)
	for i, r := range runes {
		switch {
		case r == '-' || r == '_' || r == ' ' || r == '.':
			flush()
		case r >= 'A' && r <= 'Z':
			prevUpper := i > 0 && runes[i-1] >= 'A' && runes[i-1] <= 'Z'
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if !prevUpper || nextLower {
				flush()
			}
			cur.WriteRune(r)
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return words
}
