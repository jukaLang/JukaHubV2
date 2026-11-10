package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

// The expected outputs were traced by hand through webdom_module.go and
// checked to be valid JavaScript that links up with a fake __jh under node.
// webJSModTestBlank stands for removed source: every byte a space, line
// breaks kept. webJSModTestGone stands for removed statements, one per line.

const (
	webJSModTestHead = `(function (__jh) { return (async function () { "use strict";`
	webJSModTestTail = "\n})(); })"
)

func webJSModTestBlank(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c != '\n' && c != '\r' {
			b[i] = ' '
		}
	}
	return string(b)
}

// webJSModTestGone is what the statements stmts, written on consecutive
// lines, become once removed: each a ";" (keeping the lines around it apart)
// followed by its blanked source.
func webJSModTestGone(stmts ...string) string {
	out := make([]string, len(stmts))
	for i, s := range stmts {
		out[i] = ";" + webJSModTestBlank(s)
	}
	return strings.Join(out, "\n")
}

func webJSModTestSameImports(a, b []webJSImport) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestWebJSModTransform(t *testing.T) {
	blank, gone := webJSModTestBlank, webJSModTestGone
	importLines := []string{
		"import def, * as ns from './dep.js';",
		"import { a, b as c, \"x-y\" as xy, default as d2 } from \"./lib.js\";",
		"import './side.js';",
		"import more from \"./lib.js\";",
	}
	importForms := strings.Join(importLines, "\n") + "\nuse(def, ns, a, c, xy, d2, more);"
	exportDecls := "export let n = 1, { p, q: [r], ...s } = obj;\n" +
		"export function f() { return n; }\n" +
		"export async function* g() {}\n" +
		"export class K {}\n" +
		"export const h = () => {\n" +
		"  return import.meta.url;\n" +
		"}"
	reexportLines := []string{
		"export { a as b, default } from \"./m.js\";",
		"export * from './n.js';",
		"export * as ns from \"./m.js\";",
		"export { x as \"y-z\" };",
	}
	reexports := strings.Join(reexportLines, "\n") + "\nlet x;"
	attrLines := []string{
		"import data from \"./d.json\" with { type: \"json\" };",
		"import sheet from \"./s.css\" assert { type: 'css' };",
		"import again from \"./d.json\";",
		"export { default as cfg } from \"./d.json\" with { \"type\": \"json\" };",
	}
	attrs := strings.Join(attrLines, "\n")
	// import/export inside strings, comments, templates and regexes, as
	// property names and as a method name; a regex after if (…) and after
	// "=", divisions after names and numbers.
	traps := "const s = \"import x from 'y'\"; // export default 1\n" +
		"/* import(\"no\") */ const t = `a${`b${ {c: 1}.c }`}import.meta`;\n" +
		"const r = /[/]import\\(/g, q = a / 2 / b;\n" +
		"const o = { import: 1, export: 2 }, m = o.import, k = { import(x) { return x; } };\n" +
		"if (s) /export/.test(t);"
	commentedImport := "import {\n  a, // first\n  b /* second\n  line */\n} from \"./x.js\";"
	commented := commentedImport + "\nexport { a };"
	tests := []struct {
		name    string
		src     string
		want    string // the output between webJSModTestHead and webJSModTestTail
		imports []webJSImport
	}{
		{
			"rollup chunk",
			`import{a as b}from"./x.js";const c=b(1);export{c as d};`,
			` __jh.exp({ "d": () => c }); const { a: b } = __jh.imp(0); ` + gone(`import{a as b}from"./x.js";`) + `const c=b(1);` + gone(`export{c as d};`),
			[]webJSImport{{Spec: "./x.js"}},
		},
		{
			"vite entry",
			`import{_ as p}from"./preload.js";const u=new URL("./a.png",import.meta.url).href;p(()=>import("./page.js"));`,
			` const { _: p } = __jh.imp(0); ` + gone(`import{_ as p}from"./preload.js";`) + `const u=new URL("./a.png",__jh.meta.url).href;p(()=>__jh.dyn("./page.js"));`,
			[]webJSImport{{Spec: "./preload.js"}},
		},
		{
			"import forms",
			importForms,
			` const def = __jh.imp(0).default; const ns = __jh.imp(0); const { a: a, b: c, "x-y": xy, default: d2 } = __jh.imp(1); const more = __jh.imp(1).default; ` + gone(importLines...) + "\nuse(def, ns, a, c, xy, d2, more);",
			[]webJSImport{{Spec: "./dep.js"}, {Spec: "./lib.js"}, {Spec: "./side.js"}},
		},
		{
			"export declarations",
			exportDecls,
			` __jh.exp({ "n": () => n, "p": () => p, "r": () => r, "s": () => s, "f": () => f, "g": () => g, "K": () => K, "h": () => h }); ` + blank("export") + " let n = 1, { p, q: [r], ...s } = obj;\n" + blank("export") + " function f() { return n; }\n" + blank("export") + " async function* g() {}\n" + blank("export") + " class K {}\n" + blank("export") + " const h = () => {\n  return __jh.meta.url;\n}",
			nil,
		},
		{
			"anonymous default function",
			"export default function () {}\n(foo)",
			` __jh.exp({ "default": () => __jh_default }); const __jh_default = function () {};` + "\n(foo)",
			nil,
		},
		{
			"anonymous default class",
			"export default class extends Base {}",
			` __jh.exp({ "default": () => __jh_default }); const __jh_default = class extends Base {};`,
			nil,
		},
		{
			"named default",
			"export default async function load() {}\nload.x = 1;",
			` __jh.exp({ "default": () => load }); ` + blank("export default") + " async function load() {}\nload.x = 1;",
			nil,
		},
		{
			"default expression",
			"export\ndefault {\n  a: 1\n}",
			` __jh.exp({ "default": () => __jh_default }); const __jh_default =` + "\n {\n  a: 1\n}",
			nil,
		},
		{
			"re-exports",
			reexports,
			` __jh.exp({ "ns": () => __jh.imp(0), "y-z": () => x }); __jh.reexp(0, { "b": "a", "default": "default" }); __jh.star(1); ` + gone(reexportLines...) + "\nlet x;",
			[]webJSImport{{Spec: "./m.js"}, {Spec: "./n.js"}},
		},
		{
			"import attributes",
			attrs,
			` __jh.reexp(0, { "cfg": "default" }); const data = __jh.imp(0).default; const sheet = __jh.imp(1).default; const again = __jh.imp(2).default; ` + gone(attrLines...),
			[]webJSImport{{Spec: "./d.json", Type: "json"}, {Spec: "./s.css", Type: "css"}, {Spec: "./d.json"}},
		},
		{
			"lexer traps",
			traps,
			" " + traps,
			nil,
		},
		{
			"regex holding a brace",
			"function f(x) { return /}/.test(x) ? x / 2 : 0; }\nexport { f };",
			` __jh.exp({ "f": () => f }); function f(x) { return /}/.test(x) ? x / 2 : 0; }` + "\n" + gone("export { f };"),
			nil,
		},
		{
			"hashbang and byte order mark",
			"\xef\xbb\xbf#!/usr/bin/env node\nexport default 1\n",
			` __jh.exp({ "default": () => __jh_default }); ` + blank("\xef\xbb\xbf#!/usr/bin/env node") + "\nconst __jh_default = 1\n",
			nil,
		},
		{
			"line breaks end initializers",
			"export const a = 1\nconst b = 2, c = 3\nexport let d = function ()\n{\n  return 1\n}, e = 4",
			` __jh.exp({ "a": () => a, "d": () => d, "e": () => e }); ` + blank("export") + " const a = 1\nconst b = 2, c = 3\n" + blank("export") + " let d = function ()\n{\n  return 1\n}, e = 4",
			nil,
		},
		{
			"comments in a removed import",
			commented,
			` __jh.exp({ "a": () => a }); const { a: a, b: b } = __jh.imp(0); ` + gone(commentedImport, "export { a };"),
			[]webJSImport{{Spec: "./x.js"}},
		},
		{
			"templates and dynamic import",
			"export default `${import.meta.url}`;\nconst load = () => import(`./${name}.js`);",
			` __jh.exp({ "default": () => __jh_default }); const __jh_default = ` + "`${__jh.meta.url}`;\nconst load = () => __jh.dyn(`./${name}.js`);",
			nil,
		},
		{
			"string and escaped export names",
			"let a, b;\nexport { a as \"a-b\", b as \\u{63} };",
			` __jh.exp({ "a-b": () => a, "c": () => b }); let a, b;` + "\n" + gone("export { a as \"a-b\", b as \\u{63} };"),
			nil,
		},
		{
			"bare import",
			`import "./polyfill.js"`,
			" " + gone(`import "./polyfill.js"`),
			[]webJSImport{{Spec: "./polyfill.js"}},
		},
		{
			"empty module",
			"",
			" ",
			nil,
		},
		// Removed statements ended by a line break alone: without the ";" the
		// lines around them would join into a call of 1, a "use client"[1,2]
		// index and a 1[a] index.
		{
			"removed export star between lines",
			"const a = 1\nexport * from \"./x.js\"\n(function(){})()",
			` __jh.star(0); const a = 1` + "\n" + gone(`export * from "./x.js"`) + "\n(function(){})()",
			[]webJSImport{{Spec: "./x.js"}},
		},
		{
			"removed import after a directive",
			"\"use client\"\nimport x from \"./x.js\"\n[1,2].forEach(f)",
			` const x = __jh.imp(0).default; "use client"` + "\n" + gone(`import x from "./x.js"`) + "\n[1,2].forEach(f)",
			[]webJSImport{{Spec: "./x.js"}},
		},
		{
			"removed export list between lines",
			"let a = 1\nexport { a }\n[a].map(String)",
			` __jh.exp({ "a": () => a }); let a = 1` + "\n" + gone("export { a }") + "\n[a].map(String)",
			nil,
		},
		// "of" is a common minified name: a "/" after it divides, except right
		// after the left-hand side in a for-of head.
		{
			"variable named of",
			`let of=4,q=2;let z=of/q;let s="/";export{z}`,
			` __jh.exp({ "z": () => z }); let of=4,q=2;let z=of/q;let s="/";` + gone("export{z}"),
			nil,
		},
		{
			"of in for heads",
			"let of = 4, n = 0;\nfor (let i = of / 2; i < of; i++) n += of / 2;\nfor (const c of /\"/.exec(\"a\\\"\") || []) n++;\nexport { n };",
			` __jh.exp({ "n": () => n }); let of = 4, n = 0;` + "\nfor (let i = of / 2; i < of; i++) n += of / 2;\nfor (const c of /\"/.exec(\"a\\\"\") || []) n++;\n" + gone("export { n };"),
			nil,
		},
		{
			"regex after a for await head",
			"for await (const x of y) /\"/.test(x)\nexport {}",
			` for await (const x of y) /"/.test(x)` + "\n" + gone("export {}"),
			nil,
		},
		// As a plain key, "__proto__" would set the literal's prototype.
		{
			"__proto__ namespace export",
			`export * as __proto__ from "./m.js";`,
			` __jh.exp({ ["__proto__"]: () => __jh.imp(0) }); ` + gone(`export * as __proto__ from "./m.js";`),
			[]webJSImport{{Spec: "./m.js"}},
		},
		{
			"__proto__ re-export",
			`export { a as __proto__ } from "./m.js";`,
			` __jh.reexp(0, { ["__proto__"]: "a" }); ` + gone(`export { a as __proto__ } from "./m.js";`),
			[]webJSImport{{Spec: "./m.js"}},
		},
		{
			"__proto__ escaped in a string export name",
			"let a;\nexport { a as \"\\u005f_proto__\" };",
			` __jh.exp({ ["__proto__"]: () => a }); let a;` + "\n" + gone("export { a as \"\\u005f_proto__\" };"),
			nil,
		},
		{
			"__proto__ function export",
			"export function __proto__() {}",
			` __jh.exp({ ["__proto__"]: () => __proto__ }); ` + blank("export") + " function __proto__() {}",
			nil,
		},
	}
	for _, tc := range tests {
		code, imports, err := webJSTransformModule(tc.src)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if want := webJSModTestHead + tc.want + webJSModTestTail; code != want {
			t.Errorf("%s:\n got %q\nwant %q", tc.name, code, want)
		}
		if !webJSModTestSameImports(imports, tc.imports) {
			t.Errorf("%s: imports %q, want %q", tc.name, imports, tc.imports)
		}
		// The body keeps every line where it was: one more line break (the
		// one before the closing "})(); })") and none lost.
		if got, want := strings.Count(code, "\n"), strings.Count(tc.src, "\n")+1; got != want {
			t.Errorf("%s: %d line breaks, want %d", tc.name, got, want)
		}
	}
}

func TestWebJSModTransformErrors(t *testing.T) {
	for _, src := range []string{
		`const a = "x;`,
		"const t = `abc",
		"const t = `${a`",
		"function f() {",
		"a = b)",
		"a = [1, 2}",
		"/* abc",
		"x = /abc",
		"export foo;",
		`import {a} "x";`,
		`import * from "x";`,
		`export { "a" };`,
		"export default",
	} {
		code, imports, err := webJSTransformModule(src)
		if err == nil || code != "" || imports != nil {
			t.Errorf("%q: got (%q, %v, %v), want an error", src, code, imports, err)
		}
	}
	// The error names the line, which the loader shows in the script error.
	if _, _, err := webJSTransformModule("const a = 1;\nconst b = \"x;"); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error %v does not name line 2", err)
	}
	// Lines end where JavaScript ends them: CR, CR LF (one break), U+2028.
	if _, _, err := webJSTransformModule("a;\rb;\r\nc;\u2028const d = \"x;"); err == nil || !strings.Contains(err.Error(), "line 4") {
		t.Errorf("error %v does not name line 4", err)
	}
}

// Hostile modules come back, as code or an error, in linear time and without
// deep Go recursion: the script watchdog cannot interrupt Go code, and a
// stack overflow kills the process. These once took cubic time (about 100 KB
// of class heritages without semicolons ran for minutes) or a million nested
// Go frames.
func TestWebJSModHostile(t *testing.T) {
	nested := strings.Repeat("class extends ", 3000) + "B" + strings.Repeat(" {}", 3000)
	for _, tc := range []struct {
		name    string
		src     string
		wantErr bool
	}{
		{"class heritages without semicolons", strings.Repeat("export const a = class extends b ", 3000), false},
		{"nested brackets", "export const " + strings.Repeat("[", 1<<20) + "a" + strings.Repeat("]", 1<<20) + " = x", true},
		{"nested template substitutions", strings.Repeat("`${", 1<<15) + strings.Repeat("}`", 1<<15), true},
		{"nested class heritages", "export default " + nested, true},
		{"nested class heritages in an initializer", "export const a = " + nested, true},
	} {
		done := make(chan error, 1)
		go func(src string) {
			_, _, err := webJSTransformModule(src)
			done <- err
		}(tc.src)
		select {
		case err := <-done:
			if (err != nil) != tc.wantErr {
				t.Errorf("%s: error %v, want an error: %v", tc.name, err, tc.wantErr)
			}
		case <-time.After(30 * time.Second):
			t.Fatalf("%s: still running after 30 s", tc.name)
		}
	}

	// Distinct imports are found through a map (a linear search made 3 MiB
	// of them quadratic) and capped.
	var many strings.Builder
	for i := 0; i < webJSModMaxImports; i++ {
		many.WriteString(`import"a` + strconv.Itoa(i) + `";`)
	}
	many.WriteString(`import"a0";`) // seen before: no new entry
	if _, imports, err := webJSTransformModule(many.String()); err != nil || len(imports) != webJSModMaxImports || imports[1].Spec != "a1" {
		t.Errorf("%d imports: got %d, error %v", webJSModMaxImports, len(imports), err)
	}
	many.WriteString(`import"one more";`)
	if _, _, err := webJSTransformModule(many.String()); err == nil {
		t.Errorf("%d imports: no error", webJSModMaxImports+1)
	}
}

func TestWebJSModRewriteDynamicImport(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want string
		ok   bool
	}{
		{
			`import("./a.js").then(m => m.run(import.meta.url));`,
			`__jh_import("./a.js").then(m => m.run(({url: document.baseURI}).url));`,
			true,
		},
		{"var s = \"import('x')\"; // import(y)\nobj.import(1); var o = { import: 2 };", "", false},
		{"var a = 1;", "", false},
		{`var s = "import(`, "", false},
		// Classic scripts may hide in HTML comments; the apostrophe would
		// otherwise open a string.
		{"<!-- don't run\nimport(\"./m.js\");\n-->", "<!-- don't run\n__jh_import(\"./m.js\");\n-->", true},
		{"class A { import(x) { return import(x); } }", "class A { import(x) { return __jh_import(x); } }", true},
		{"x = import\n.meta;", "x = ({url: document.baseURI})\n;", true},
		{"#!/bin/node\nimport('./a.js');", "#!/bin/node\n__jh_import('./a.js');", true},
	} {
		want := tc.want
		if !tc.ok {
			want = tc.src
		}
		if out, ok := webJSRewriteDynamicImport(tc.src); out != want || ok != tc.ok {
			t.Errorf("%q:\n got (%q, %v)\nwant (%q, %v)", tc.src, out, ok, want, tc.ok)
		}
	}
}

func TestWebJSModStringValue(t *testing.T) {
	const bs = "\\"
	for _, tc := range [][2]string{
		{`"plain"`, "plain"},
		{`'it\'s'`, "it's"},
		{`"a\x41b\tc"`, "aAb\tc"},
		{`"\u{1F600}"`, "\xf0\x9f\x98\x80"},
		{`"` + bs + `uD83D` + bs + `uDE00"`, "\xf0\x9f\x98\x80"},
		{`"` + bs + `u0041z"`, "Az"},
		{"\"a\\\nb\"", "ab"},
	} {
		if got := webJSModStringValue(tc[0]); got != tc[1] {
			t.Errorf("webJSModStringValue(%s) = %q, want %q", tc[0], got, tc[1])
		}
	}
	if got := webJSModIdentValue("a" + bs + "u0062" + bs + "u{63}"); got != "abc" {
		t.Errorf("webJSModIdentValue = %q, want abc", got)
	}
	if got, want := webJSModQuote("a\"b\\c\nd"), `"a\"b\\c`+bs+`u000ad"`; got != want {
		t.Errorf("webJSModQuote = %s, want %s", got, want)
	}
}

// Every prefix of a module that uses everything the transform knows must
// come back as a result or an error, never a panic or a hang.
func TestWebJSModNoPanic(t *testing.T) {
	src := "\xef\xbb\xbf#!/x\n" +
		"import d, * as ns from './a.js' with { type: 'json' };\n" +
		"import { a as b, \"s-t\" as u, default as v } from \"./b.js\";\n" +
		"export const { p, q: [r = 1, ...s], ...t } = ns, w = function () {}\n" +
		"export default class extends (b ? B : C) { static #x = /[}]/g; m() { return import.meta.url } }\n" +
		"export * as nn from './c.js'; export * from './d.js'; export { b as \"bb\", u };\n" +
		"const \xc3\xbc = `\xc3\xa9${`x${ {y: 1}.y }`}` / 2, re = a ? /x/ : 1e-3, z = 0x1_0n;\n" +
		"if (z) /re/.test(\"import(\") && import(\"./e.js\").then(m => m.f?.(.5));\n"
	for i := 0; i <= len(src); i++ {
		webJSTransformModule(src[:i])
		webJSRewriteDynamicImport(src[:i])
		webJSTransformModule(src[i:])
	}
	if _, _, err := webJSTransformModule(src); err != nil {
		t.Errorf("whole module: %v", err)
	}
}
