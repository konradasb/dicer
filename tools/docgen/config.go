// Copyright 2026 Dicer Authors
// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/konradasb/dicer/internal/cli"
)

// module is this module's path.
const module = "github.com/konradasb/dicer"

// Packages declaring the types a configuration is read into.
const (
	composePackage = module + "/internal/compose"
	daemonPackage  = module + "/internal/daemon"
)

// configurationSection is part of a configuration reference page: an
// introduction, then the keys of the struct structName in the package at
// importPath, under a prefix. A section with no struct is its introduction
// alone, or what build returns, given every key and every command.
type configurationSection struct {
	intro, importPath, structName, prefix string
	build                                 func(keys, commands map[string]bool) string
}

// writeConfiguration writes the configuration reference: every key of the
// daemon's configuration file.
func writeConfiguration(root, dir string) error {
	l := &loader{
		root:     root,
		packages: map[string]*goPackage{},
		keys:     map[string]bool{},
		commands: commandPaths(cli.NewCommand()),
	}

	pages := []struct {
		path        string
		frontMatter frontMatter
		sections    []configurationSection
	}{
		{
			path: filepath.Join(dir, "configuration.md"),
			frontMatter: frontMatter{
				title: "Daemon configuration", weight: 3, icon: "cog",
				description: "Every key of dicerd's configuration file, with its default.",
			},
			sections: []configurationSection{{
				intro: "`dicerd` reads its configuration from `/etc/dicerd/config.yaml`, or the file " +
					"`dicerd serve --config` names, when it starts: a changed file is taken up by restarting " +
					"it. The file is optional, as every key has a default, and unknown keys are rejected: " +
					"check a change with `sudo dicerd validate` before `sudo systemctl restart dicerd`.\n\n" +
					"```yaml\n" +
					"data_dir: /var/lib/dicer\n" +
					"api:\n" +
					"  socket:\n" +
					"    group: dicer\n" +
					"  tcp:\n" +
					"    listen: 0.0.0.0:7443\n" +
					"    tls:\n" +
					"      cert_file: /etc/dicerd/tls/server.pem\n" +
					"      key_file: /etc/dicerd/tls/server-key.pem\n" +
					"      client_ca_file: /etc/dicerd/tls/ca.pem\n" +
					"metrics:\n" +
					"  enable: true\n" +
					"images:\n" +
					"  gc_max_size: 50GiB\n" +
					"```\n\n" +
					"## General {#general}\n\n" +
					"The daemon's own settings. The sections after them are the API, resources, networking, " +
					"defaults, metrics, images, events and registries.",
				importPath: daemonPackage, structName: "Config",
			}},
		},
		{
			path: filepath.Join(dir, "compose-file.md"),
			frontMatter: frontMatter{
				title: "Compose file", weight: 4, icon: "template",
				description: "Every key of the file dicer compose reads, with its default.",
				related:     []string{"/docs/guides/compose", "/docs/reference/cli/dicer_compose"},
			},
			sections: []configurationSection{
				{
					intro: "`dicer compose` reads a project from a YAML file: its services, and the networks and " +
						"volumes they use. A key it does not support is refused, with the reason, rather than " +
						"ignored. `dicer compose config` checks a file and shows it as it is read. See " +
						"[Compose]({{< relref \"/docs/guides/compose\" >}}) for a walk through one.\n\n" +
						"## The file {#the-file}\n\n" +
						"`dicer compose` looks for `dicer-compose.yaml`, `dicer-compose.yml`, `compose.yaml` and " +
						"`compose.yml`, in that order, in the current directory and then in each directory above " +
						"it. `-f` or `$DICER_COMPOSE_FILE` names another. Keys starting `x-` are extensions: " +
						"ignored, and where YAML anchors are defined to share settings between services.\n\n" +
						"## Names {#names}\n\n" +
						"A project's instances are named `PROJECT-SERVICE`, and its networks and volumes " +
						"`PROJECT-NAME`, so each belongs visibly to its project and two projects on a host do not " +
						"collide. Names are letters, digits and hyphens: `db_primary` cannot be a service's name.\n\n" +
						"Each instance is labelled `dicer.compose.project` and `dicer.compose.service`, which is " +
						"how `dicer compose` finds its instances again, and `dicer.compose.config-hash`, a digest " +
						"of its definition, which is how `dicer compose up` tells a changed service from one it " +
						"can leave running. Labels starting `dicer.compose.` are reserved for `dicer compose`.\n\n" +
						"## General {#general}\n\n" +
						"The project's own keys. The sections after them are its services, networks and volumes.",
					importPath: composePackage, structName: "rawFile",
				},
				{intro: composeVariables},
				{build: composeUnsupported},
			},
		},
	}

	for _, p := range pages {
		for _, section := range p.sections {
			if section.structName == "" {
				continue
			}
			if err := l.collectKeys(section.importPath, section.structName, section.prefix); err != nil {
				return fmt.Errorf("%s: %w", filepath.Base(p.path), err)
			}
		}
	}

	for _, p := range pages {
		var body bytes.Buffer

		for _, section := range p.sections {
			if section.build != nil {
				body.WriteString(section.build(l.keys, l.commands) + "\n")
				continue
			}
			if section.structName == "" {
				body.WriteString(section.intro + "\n")
				continue
			}

			b, err := l.document(section.importPath, section.structName, section.prefix, section.intro)
			if err != nil {
				return fmt.Errorf("%s: %w", filepath.Base(p.path), err)
			}
			body.Write(b)
		}

		if err := writePage(p.path, p.frontMatter, body.Bytes()); err != nil {
			return err
		}
	}

	return nil
}

// loader parses the packages a configuration's types are declared in.
type loader struct {
	root     string
	fset     token.FileSet
	packages map[string]*goPackage

	// keys are every key of every page, alone and under its prefix, and
	// commands every dicer command: what the text writes as code.
	keys, commands map[string]bool
}

// goPackage is a package's struct types.
type goPackage struct {
	path    string
	structs map[string]*structType
}

// structType is a struct as declared, with the imports its fields' types
// refer to.
type structType struct {
	goPackage *goPackage
	name      string
	fields    []*ast.Field
	imports   map[string]string
}

// load parses a package of this module, without its tests.
func (l *loader) load(importPath string) (*goPackage, error) {
	if p, ok := l.packages[importPath]; ok {
		return p, nil
	}

	rel, ok := strings.CutPrefix(importPath, module+"/")
	if !ok {
		return nil, fmt.Errorf("%s is not part of %s", importPath, module)
	}

	dir := filepath.Join(l.root, filepath.FromSlash(rel))

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	p := &goPackage{path: importPath, structs: map[string]*structType{}}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, err := parser.ParseFile(&l.fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}

		imports := map[string]string{}
		for _, importSpec := range file.Imports {
			imported, _ := strconv.Unquote(importSpec.Path.Value)
			alias := packageName(imported)
			if importSpec.Name != nil {
				alias = importSpec.Name.Name
			}
			imports[alias] = imported
		}

		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}

			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if st, ok := ts.Type.(*ast.StructType); ok {
					p.structs[ts.Name.Name] = &structType{
						goPackage: p, name: ts.Name.Name, fields: st.Fields.List, imports: imports,
					}
				}
			}
		}
	}

	l.packages[importPath] = p

	return p, nil
}

// packageName returns the default name of the package at importPath: the last
// element of its path, less a version suffix.
func packageName(importPath string) string {
	name := path.Base(importPath)
	if i := strings.Index(name, ".v"); i > 0 {
		name = name[:i]
	}

	return name
}

// structType returns the struct called name in the package at importPath.
func (l *loader) structType(importPath, name string) (*structType, error) {
	p, err := l.load(importPath)
	if err != nil {
		return nil, err
	}

	st, ok := p.structs[name]
	if !ok {
		return nil, fmt.Errorf("no struct %s in %s", name, importPath)
	}

	return st, nil
}

// document returns the reference for a struct and every mapping under it, with
// its keys under prefix.
func (l *loader) document(importPath, name, prefix, intro string) ([]byte, error) {
	root, err := l.structType(importPath, name)
	if err != nil {
		return nil, err
	}

	var out bytes.Buffer

	out.WriteString(intro + "\n")

	if err := l.writeMapping(&out, root, prefix, map[*structType]string{}); err != nil {
		return nil, err
	}

	return out.Bytes(), nil
}

// collectKeys adds a struct's keys, and those of every mapping under it, to
// l.keys: each alone, and under its prefix.
func (l *loader) collectKeys(importPath, name, prefix string) error {
	st, err := l.structType(importPath, name)
	if err != nil {
		return err
	}

	return l.collectStruct(st, prefix, map[*structType]bool{})
}

// collectStruct adds st's keys under prefix, and those of every mapping under
// it, to l.keys, visiting each struct once.
func (l *loader) collectStruct(st *structType, prefix string, seen map[*structType]bool) error {
	if seen[st] {
		return nil
	}
	seen[st] = true

	keys := yamlKeys(st)
	for _, f := range st.fields {
		if len(f.Names) == 0 {
			continue
		}
		key, ok := keys[f.Names[0].Name]
		if !ok {
			continue
		}

		full := key
		if prefix != "" {
			full = prefix + "." + key
		}
		l.keys[key], l.keys[full] = true, true

		_, ref, err := l.describe(st, f.Type)
		if err != nil {
			return err
		}
		if ref != nil {
			if err := l.collectStruct(ref, full, seen); err != nil {
				return err
			}
		}
	}

	return nil
}

// entry is a key of a mapping, ready to write: the key alone and under its
// prefix, its type as describe gives it, and its text.
type entry struct {
	key, full, typeDescription, text string

	// mapping is the struct of a key that is a mapping, or a list of them.
	mapping *structType
}

// writeMapping writes a mapping's plain keys, then each of its mappings as a
// section of its own, so that every key is read under its mapping. A struct
// already documented is linked to rather than written again.
func (l *loader) writeMapping(out *bytes.Buffer, st *structType, prefix string, seen map[*structType]string) error {
	entries, err := l.entries(st, prefix)
	if err != nil {
		return err
	}

	seen[st] = prefix

	for _, e := range entries {
		if e.mapping == nil {
			fmt.Fprintf(out, "\n### `%s` {#%s}\n\n*%s*\n\n%s\n", e.full, anchor(e.full), e.typeDescription, e.text)
		}
	}

	for _, e := range entries {
		if e.mapping == nil {
			continue
		}

		fmt.Fprintf(out, "\n## `%s` {#%s}\n\n*%s*\n\n%s\n", e.full, anchor(e.full), e.typeDescription, e.text)

		if other, ok := seen[e.mapping]; ok {
			fmt.Fprintf(out, "\nIts keys are the same as [`%s`](#%s)'s.\n", other, anchor(other))
			continue
		}

		if err := l.writeMapping(out, e.mapping, e.full, seen); err != nil {
			return err
		}
	}

	return nil
}

// entries returns a mapping's keys, in declaration order.
func (l *loader) entries(st *structType, prefix string) ([]entry, error) {
	keys := yamlKeys(st)

	var (
		out      []entry
		previous string
		lastLine int
	)

	for _, f := range st.fields {
		if len(f.Names) == 0 {
			return nil, fmt.Errorf("%s embeds a field, which is not documented", st.name)
		}

		goName := f.Names[0].Name
		key, ok := keys[goName]
		if !ok {
			continue
		}

		full := key
		if prefix != "" {
			full = prefix + "." + key
		}

		typeDescription, ref, err := l.describe(st, f.Type)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", st.name, goName, err)
		}
		switch {
		case ref != nil && strings.HasPrefix(typeDescription, "list of"):
			full += "[]"
		case ref != nil && strings.HasPrefix(typeDescription, "mapping of"):
			full += ".*"
		}

		line := l.fset.Position(f.Pos()).Line

		var text string
		switch {
		case f.Doc != nil:
			text = asCode(prose(f.Doc.Text(), keys), l.keys, l.commands)
		case previous != "" && line == lastLine+1:
			// Declared on the line after a documented field, which
			// documents both.
			text = fmt.Sprintf("See `%s`, above.", previous)
		default:
			return nil, fmt.Errorf("%s.%s has no doc comment, so %s would go undocumented", st.name, goName, full)
		}

		out = append(out, entry{key: key, full: full, typeDescription: typeDescription, text: text, mapping: ref})
		previous, lastLine = key, l.fset.Position(f.End()).Line
	}

	return out, nil
}

// yamlKeys maps a struct's fields to their YAML keys, leaving out those not
// read from a file.
func yamlKeys(st *structType) map[string]string {
	keys := map[string]string{}

	for _, f := range st.fields {
		if f.Tag == nil || len(f.Names) == 0 || !f.Names[0].IsExported() {
			continue
		}

		tag, _ := strconv.Unquote(f.Tag.Value)
		key, _, _ := strings.Cut(reflect.StructTag(tag).Get("yaml"), ",")
		if key == "" || key == "-" {
			continue
		}

		keys[f.Names[0].Name] = key
	}

	return keys
}

// describe returns a field's type as a configuration writes it, and the struct
// it is a mapping of, if any.
func (l *loader) describe(in *structType, expr ast.Expr) (string, *structType, error) {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return l.describe(in, t.X)

	case *ast.ArrayType:
		elem, ref, err := l.describe(in, t.Elt)
		if err != nil {
			return "", nil, err
		}
		if ref != nil && shortForms[ref.goPackage.path+"."+ref.name] {
			return "list of strings or mappings", ref, nil
		}
		if ref != nil {
			return "list of mappings", ref, nil
		}

		return "list of " + plural(elem), nil, nil

	case *ast.MapType:
		value, ref, err := l.describe(in, t.Value)
		if err != nil {
			return "", nil, err
		}
		if ref != nil {
			return "mapping of names to mappings", ref, nil
		}

		return "mapping of " + plural(value), nil, nil

	case *ast.Ident:
		if scalar, ok := scalars[t.Name]; ok {
			return scalar, nil, nil
		}

		return l.named(in.goPackage.path, t.Name)

	case *ast.SelectorExpr:
		packageIdent, ok := t.X.(*ast.Ident)
		if !ok {
			break
		}

		importPath, ok := in.imports[packageIdent.Name]
		if !ok {
			return "", nil, fmt.Errorf("unknown package %s", packageIdent.Name)
		}

		return l.named(importPath, t.Sel.Name)
	}

	return "", nil, fmt.Errorf("cannot describe a %T", expr)
}

// named describes a named type: a special one, or a struct of this module.
func (l *loader) named(importPath, name string) (string, *structType, error) {
	if special, ok := specials[importPath+"."+name]; ok {
		return special, nil, nil
	}

	if !strings.HasPrefix(importPath, module+"/") {
		return "", nil, fmt.Errorf("%s.%s is not a type a configuration can hold", importPath, name)
	}

	st, err := l.structType(importPath, name)
	if err != nil {
		return "", nil, err
	}

	return "mapping", st, nil
}

// scalars maps Go's basic types to how a configuration writes them.
var scalars = map[string]string{
	"string":  "string",
	"bool":    "boolean",
	"int":     "integer",
	"int32":   "integer",
	"int64":   "integer",
	"uint32":  "integer",
	"float64": "number",
}

// specials maps named types to how a configuration writes them.
var specials = map[string]string{
	"time.Duration":                     "duration, such as 30s or 5m",
	daemonPackage + ".byteSize":         "size, such as 512MiB or 50GiB",
	composePackage + ".byteSize":        "size, such as 512MiB or 2GiB",
	composePackage + ".stringList":      "string, or list of strings",
	composePackage + ".shellCommand":    "string, or list of strings",
	composePackage + ".healthCheckTest": "string, or list of strings",
	composePackage + ".keyValues":       "mapping of strings, or list of KEY=VALUE strings",
	composePackage + ".serviceNetworks": "list of one name, or mapping of one name to a mapping",
	composePackage + ".dependsOn":       "list of names, or mapping of names to mappings",
}

// shortForms are the structs a list may also hold as strings, each in a
// short form the field's doc comment gives.
var shortForms = map[string]bool{
	composePackage + ".rawPort":  true,
	composePackage + ".rawMount": true,
}

// plural returns a type description in the plural.
func plural(s string) string {
	switch {
	case strings.HasSuffix(s, "s"):
		return s
	case strings.Contains(s, ","), strings.Contains(s, ":"):
		return s
	default:
		return s + "s"
	}
}

// prose turns a doc comment into the reference's text, with the struct's Go
// field names written as their keys. The comment's first word is always the
// field's name; elsewhere only compound names such as TokenPath are replaced,
// as a single word such as Token may be meant as the word.
func prose(comment string, keys map[string]string) string {
	if first, rest, ok := strings.Cut(comment, " "); ok {
		if key, ok := keys[first]; ok {
			comment = "`" + key + "` " + rest
		}
	}

	for goName, key := range keys {
		if !compound.MatchString(goName) {
			continue
		}
		comment = regexp.MustCompile(`\b`+regexp.QuoteMeta(goName)+`\b`).
			ReplaceAllString(comment, "`"+key+"`")
	}

	var paragraphs []string
	for paragraph := range strings.SplitSeq(strings.TrimSpace(comment), "\n\n") {
		if isCode(paragraph) {
			paragraphs = append(paragraphs, "```yaml\n"+dedent(paragraph)+"\n```")
			continue
		}
		paragraphs = append(paragraphs, strings.Join(strings.Fields(paragraph), " "))
	}

	return strings.Join(paragraphs, "\n\n")
}

// compound matches a name made of several words.
var compound = regexp.MustCompile(`^[A-Z][a-z0-9]+[A-Z]`)

// isCode reports whether a paragraph is an indented example.
func isCode(paragraph string) bool {
	for line := range strings.SplitSeq(paragraph, "\n") {
		if line != "" && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, "  ") {
			return false
		}
	}

	return true
}

// dedent removes an example's indentation.
func dedent(paragraph string) string {
	lines := strings.Split(paragraph, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimPrefix(line, "\t")
	}

	return strings.Join(lines, "\n")
}

// anchor returns a key's anchor on the page.
func anchor(key string) string {
	return strings.NewReplacer(".*", "", ".", "-", "[]", "", "_", "-").Replace(key)
}

// codeWords matches what the reference writes as code wherever the comments
// it is generated from write it plainly: a command, a flag, an absolute path,
// an environment variable, or a configuration key of more than one word.
var codeWords = regexp.MustCompile(
	`\bdicer(?: [a-z][a-z-]*){0,2}\b` +
		`|(?:^|[\s(])--[a-z][a-z-]*` +
		`|(?:^|[\s(])/(?:etc|run|var|usr|home|tmp|opt)/[\w./@:-]*[\w/]` +
		`|\$?\b[A-Z][A-Z0-9]*_[A-Z0-9_]+\b` +
		`|\b[a-z][a-z0-9]*(?:[._][a-z0-9]+)+\b`)

// asCode writes as code the words codeWords matches in text, outside code
// already. A key is written so only if it is one of keys, and a command only
// if it is one of commands, as the patterns match ordinary words too.
func asCode(text string, keys, commands map[string]bool) string {
	parts := strings.Split(text, "`")
	for i := 0; i < len(parts); i += 2 {
		parts[i] = replaceWords(parts[i], func(m, rest string) string {
			lead := ""
			if m[0] == ' ' || m[0] == '\t' || m[0] == '\n' || m[0] == '(' {
				lead, m = m[:1], m[1:]
			}

			switch {
			case strings.HasPrefix(m, "dicer"):
				// A command is a word of its own: not the start of
				// dicer-compose.yaml.
				if m == "dicer" && rest != "" && strings.ContainsAny(rest[:1], "-.") {
					return lead + m
				}

				// The longest command the words name.
				for w := strings.Fields(m); len(w) > 0; w = w[:len(w)-1] {
					if name := strings.Join(w, " "); commands[name] {
						return lead + "`" + name + "`" + strings.TrimPrefix(m, name)
					}
				}
				return lead + m
			case strings.HasPrefix(m, "--"), strings.HasPrefix(m, "/"):
				return lead + "`" + m + "`"
			case strings.ToUpper(m) == m:
				return lead + "`" + m + "`"
			case keys[m]:
				return lead + "`" + m + "`"
			}

			return lead + m
		})
	}

	return commandFlags.ReplaceAllString(strings.Join(parts, "`"), "`$1 $2`")
}

// commandFlags matches a command written as code and a flag after it, which
// are one piece of code: `dicer compose up --pull`.
var commandFlags = regexp.MustCompile("`(dicer [^`]*)` `(--[a-z][a-z-]*)`")

// replaceWords replaces each match of codeWords in text with what replace
// returns for it, given the text after it.
func replaceWords(text string, replace func(m, rest string) string) string {
	var out strings.Builder

	last := 0
	for _, loc := range codeWords.FindAllStringIndex(text, -1) {
		out.WriteString(text[last:loc[0]])
		out.WriteString(replace(text[loc[0]:loc[1]], text[loc[1]:]))
		last = loc[1]
	}
	out.WriteString(text[last:])

	return out.String()
}
