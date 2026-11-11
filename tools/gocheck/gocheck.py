#!/usr/bin/env python3
"""gocheck — a Go syntax + scope checker for environments without a Go toolchain.

It parses Go source with a recursive-descent parser modelled on go/parser
(including the composite-literal / block ambiguity rules), then resolves
identifiers through block scopes to report the errors gc would report that do
not need type information:

  * syntax errors (missing commas, unbalanced braces, bad statements, ...)
  * undefined identifiers (package scope = every file of the package)
  * local variables declared and not used
  * imports not used / packages used without an import
  * ':=' with no new variables on the left
  * wrong argument counts for calls to package-level functions
  * unknown keys in sdl.Rect / sdl.Color / sdl.Point literals

Usage: gocheck.py [--tests] DIR [FILE ...]
  With FILE arguments only those files are reported (the whole package is still
  parsed for the package scope).
"""

import os
import re
import sys

# ---------------------------------------------------------------- tokenizer

KEYWORDS = {
    "break", "case", "chan", "const", "continue", "default", "defer", "else",
    "fallthrough", "for", "func", "go", "goto", "if", "import", "interface",
    "map", "package", "range", "return", "select", "struct", "switch", "type",
    "var",
}

OPERATORS = sorted([
    "<<=", ">>=", "&^=", "...", "&&", "||", "<-", "++", "--", "==", "!=", "<=",
    ">=", ":=", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<", ">>", "&^",
    "~", "+", "-", "*", "/", "%", "&", "|", "^", "<", ">", "=", "!", "(", ")",
    "[", "]", "{", "}", ",", ";", ".", ":",
], key=len, reverse=True)

NUM_RE = re.compile(
    r"""(?:
        0[xX][0-9a-fA-F_]*(?:\.[0-9a-fA-F_]*)?(?:[pP][+-]?[0-9_]+)?
      | 0[bB][01_]+
      | 0[oO][0-7_]+
      | (?:[0-9][0-9_]*(?:\.[0-9_]*)?|\.[0-9][0-9_]*)(?:[eE][+-]?[0-9_]+)?
    )i?""", re.X)


class Tok:
    __slots__ = ("kind", "val", "line", "col", "auto")

    def __init__(self, kind, val, line, col, auto=False):
        self.kind = kind  # IDENT KEYWORD INT STRING CHAR OP EOF
        self.val = val
        self.line = line
        self.col = col
        self.auto = auto  # automatically inserted semicolon

    def __repr__(self):
        return "%s(%r)@%d:%d" % (self.kind, self.val, self.line, self.col)


class GoSyntaxError(Exception):
    def __init__(self, msg, line, col=0):
        Exception.__init__(self, msg)
        self.msg = msg
        self.line = line
        self.col = col


def tokenize(src):
    toks = []
    comment_lines = set()
    i = 0
    n = len(src)
    line = 1
    col = 1
    last = None  # last significant token (for semicolon insertion)

    def needs_semi(t):
        if t is None:
            return False
        if t.kind in ("IDENT", "INT", "STRING", "CHAR"):
            return True
        if t.kind == "KEYWORD" and t.val in ("break", "continue", "fallthrough", "return"):
            return True
        if t.kind == "OP" and t.val in ("++", "--", ")", "]", "}"):
            return True
        return False

    def newline():
        nonlocal last
        if needs_semi(last):
            t = Tok("OP", ";", line, col, auto=True)
            toks.append(t)
            last = t

    while i < n:
        c = src[i]
        if c == "\n":
            newline()
            i += 1
            line += 1
            col = 1
            continue
        if c in " \t\r﻿":
            i += 1
            col += 1
            continue
        if src.startswith("//", i):
            j = src.find("\n", i)
            if j < 0:
                j = n
            comment_lines.add(line)
            col += j - i
            i = j
            continue
        if src.startswith("/*", i):
            j = src.find("*/", i + 2)
            if j < 0:
                raise GoSyntaxError("comment not terminated", line, col)
            body = src[i:j + 2]
            nl = body.count("\n")
            for k in range(nl + 1):
                comment_lines.add(line + k)
            if nl:
                newline()
                line += nl
                col = len(body) - body.rfind("\n")
            else:
                col += len(body)
            i = j + 2
            continue
        sl, sc = line, col
        if c.isalpha() or c == "_" or ord(c) > 127 and c.isidentifier():
            j = i + 1
            while j < n and (src[j].isalnum() or src[j] == "_" or (ord(src[j]) > 127 and src[j].isidentifier())):
                j += 1
            word = src[i:j]
            kind = "KEYWORD" if word in KEYWORDS else "IDENT"
            t = Tok(kind, word, sl, sc)
            toks.append(t)
            last = t
            col += j - i
            i = j
            continue
        if c.isdigit() or (c == "." and i + 1 < n and src[i + 1].isdigit()):
            m = NUM_RE.match(src, i)
            if not m or m.end() == i:
                raise GoSyntaxError("bad number", line, col)
            t = Tok("INT", m.group(0), sl, sc)
            toks.append(t)
            last = t
            col += m.end() - i
            i = m.end()
            continue
        if c == '"':
            j = i + 1
            while j < n and src[j] != '"':
                if src[j] == "\\":
                    j += 2
                    continue
                if src[j] == "\n":
                    raise GoSyntaxError("string literal not terminated", line, col)
                j += 1
            if j >= n:
                raise GoSyntaxError("string literal not terminated", line, col)
            t = Tok("STRING", src[i:j + 1], sl, sc)
            toks.append(t)
            last = t
            col += j + 1 - i
            i = j + 1
            continue
        if c == "`":
            j = src.find("`", i + 1)
            if j < 0:
                raise GoSyntaxError("raw string literal not terminated", line, col)
            body = src[i:j + 1]
            t = Tok("STRING", body, sl, sc)
            toks.append(t)
            last = t
            nl = body.count("\n")
            if nl:
                line += nl
                col = len(body) - body.rfind("\n")
            else:
                col += len(body)
            i = j + 1
            continue
        if c == "'":
            j = i + 1
            while j < n and src[j] != "'":
                if src[j] == "\\":
                    j += 2
                    continue
                if src[j] == "\n":
                    raise GoSyntaxError("rune literal not terminated", line, col)
                j += 1
            t = Tok("CHAR", src[i:j + 1], sl, sc)
            toks.append(t)
            last = t
            col += j + 1 - i
            i = j + 1
            continue
        for op in OPERATORS:
            if src.startswith(op, i):
                t = Tok("OP", op, sl, sc)
                toks.append(t)
                last = t
                i += len(op)
                col += len(op)
                break
        else:
            raise GoSyntaxError("invalid character %r" % c, line, col)
    newline()
    toks.append(Tok("EOF", "", line, col))
    token_lines = {t.line for t in toks}
    tokenize.comment_only = {ln for ln in comment_lines if ln not in token_lines}
    return toks


# ------------------------------------------------------------------- scopes

UNIVERSE = {
    "true", "false", "nil", "iota",
    "append", "cap", "clear", "close", "complex", "copy", "delete", "imag", "len",
    "make", "max", "min", "new", "panic", "print", "println", "real", "recover",
    "bool", "byte", "complex64", "complex128", "error", "float32", "float64",
    "int", "int8", "int16", "int32", "int64", "rune", "string", "uint", "uint8",
    "uint16", "uint32", "uint64", "uintptr", "any", "comparable",
}


class VarInfo:
    __slots__ = ("name", "line", "col", "kind", "used", "fn_depth")

    def __init__(self, name, line, col, kind, fn_depth):
        self.name = name
        self.line = line
        self.col = col
        self.kind = kind  # var param const type func label import
        self.used = False
        self.fn_depth = fn_depth


class Scope:
    __slots__ = ("names", "parent", "kind")

    def __init__(self, parent, kind):
        self.names = {}
        self.parent = parent
        self.kind = kind


# Expression node kinds (minimal AST): we keep just enough to know whether an
# expression is a bare identifier / type name, for the composite literal rule,
# and for assignment-use tracking.
class Expr:
    def __init__(self, kind, name=None, tok=None, x=None):
        self.kind = kind  # ident selector call index paren unary binary lit composite func type star other
        self.name = name
        self.tok = tok
        self.x = x
        self.args = None
        self.nargs_multi = False
        self.is_type = False
        self.deferred = []
        self.is_type_switch = False
        self.op = None
        self.op_i = None
        self.y = None
        self.idx = None
        self.elts = None


class Parser:
    def __init__(self, toks, filename, report):
        self.toks = toks
        self.p = 0
        self.tok = toks[0]
        self.filename = filename
        self.report = report
        self.expr_lev = 0
        self.scope = None
        self.fn_depth = 0
        self.unresolved = []  # (name, line, col) for package-scope lookup
        self.calls = []  # (name, nargs, line, has_ellipsis, single_call_arg)
        self.file_imports = {}  # name -> VarInfo
        self.pkg_decls = {}  # name -> (kind, line, info)
        self.literal_checks = []  # (typename, key, line)
        self.locals_report = []
        self.fmt_report = []
        self.comment_only = getattr(tokenize, "comment_only", set())

    # -- token helpers
    def next(self):
        self.p += 1
        self.tok = self.toks[self.p]

    def at(self, kind, val=None):
        t = self.tok
        if t.kind != kind:
            return False
        return val is None or t.val == val

    def at_op(self, val):
        return self.tok.kind == "OP" and self.tok.val == val

    def at_kw(self, val):
        return self.tok.kind == "KEYWORD" and self.tok.val == val

    def error(self, msg, t=None):
        t = t or self.tok
        raise GoSyntaxError(msg, t.line, t.col)

    def expect_op(self, val):
        if not self.at_op(val):
            what = self.tok.val if self.tok.kind != "EOF" else "EOF"
            if self.tok.auto:
                what = "newline"
            self.error("syntax error: unexpected %s, expected %s" % (what, val))
        t = self.tok
        self.next()
        return t

    def expect_semi(self, ctx="statement"):
        if self.at_op(";"):
            self.next()
            return
        if self.at_op(")") or self.at_op("}"):
            return  # semicolon may be omitted before a closing ) or }
        if self.tok.kind == "EOF":
            return
        self.error("syntax error: unexpected %s at end of %s" % (self.tok.val, ctx))

    def ident(self):
        if self.tok.kind != "IDENT":
            what = self.tok.val if not self.tok.auto else "newline"
            self.error("syntax error: unexpected %s, expected name" % what)
        t = self.tok
        self.next()
        return t

    # -- scopes
    def open_scope(self, kind="block"):
        self.scope = Scope(self.scope, kind)

    def close_scope(self):
        s = self.scope
        for name, info in s.names.items():
            if info.kind == "var" and not info.used and name != "_":
                self.locals_report.append((info.line, info.col, "declared and not used: %s" % name))
        self.scope = s.parent

    def declare(self, tok, kind):
        name = tok.val
        if name == "_":
            return None
        info = VarInfo(name, tok.line, tok.col, kind, self.fn_depth)
        if self.scope is None:
            return info
        prev = self.scope.names.get(name)
        if prev is not None and kind != "label" and prev.kind != "label":
            self.locals_report.append((tok.line, tok.col, "%s redeclared in this block (previous at line %d)" % (name, prev.line)))
        self.scope.names[name] = info
        return info

    def lookup_local(self, name):
        s = self.scope
        while s is not None:
            info = s.names.get(name)
            if info is not None:
                return info
            s = s.parent
        return None

    def use(self, tok, assign_only=False):
        name = tok.val
        if name == "_":
            return
        info = self.lookup_local(name)
        if info is not None:
            if not assign_only:
                info.used = True
            return
        if name in self.file_imports:
            self.file_imports[name].used = True
            return
        self.unresolved.append((name, tok.line, tok.col))

    # ------------------------------------------------------------ file level
    def parse_file(self):
        if not self.at_kw("package"):
            self.error("expected package clause")
        self.next()
        self.ident()
        self.expect_semi("package clause")
        while self.at_kw("import"):
            self.next()
            if self.at_op("("):
                self.next()
                while not self.at_op(")"):
                    self.import_spec()
                    self.expect_semi("import")
                self.next()
            else:
                self.import_spec()
            self.expect_semi("import")
        while self.tok.kind != "EOF":
            self.top_decl()
        return self

    def import_spec(self):
        alias = None
        if self.tok.kind == "IDENT" or self.at_op("."):
            alias = self.tok
            self.next()
        if self.tok.kind != "STRING":
            self.error("import path must be a string")
        path = self.tok.val.strip('"`')
        line = self.tok.line
        self.next()
        if alias is not None and alias.val in ("_", "."):
            return
        name = alias.val if alias is not None else import_name(path)
        self.file_imports[name] = VarInfo(name, line, 0, "import", 0)
        self.file_imports[name].used = False
        self.file_imports[name].kind = "import:" + path

    def top_decl(self):
        t = self.tok
        if self.at_kw("func"):
            self.func_decl()
        elif self.at_kw("var") or self.at_kw("const") or self.at_kw("type"):
            self.gen_decl(top=True)
        elif self.at_kw("import"):
            self.error("syntax error: imports must appear before other declarations")
        else:
            self.error("syntax error: non-declaration statement outside function body")
        self.expect_semi("declaration")

    def add_pkg(self, tok, kind, extra=None):
        if tok.val == "_" or tok.val == "init" and kind == "func":
            return
        self.pkg_decls.setdefault(tok.val, []).append((kind, tok.line, extra))

    def func_decl(self):
        self.next()  # func
        recv = None
        self.open_scope("func")
        self.fn_depth += 1
        if self.at_op("("):
            self.receiver()
            recv = True
        name = self.ident()
        tparams = 0
        if self.at_op("["):
            tparams = self.type_params()
        nparams, variadic = self.params(declare=True)
        self.result(declare=True)
        if recv is None:
            self.add_pkg(name, "func", (nparams, variadic))
        if self.at_op("{"):
            self.expr_lev_save = self.expr_lev
            self.block_body(new_scope=False)
        self.fn_depth -= 1
        self.close_scope()

    def type_params(self):
        # [T any, K comparable]
        self.expect_op("[")
        count = 0
        while not self.at_op("]"):
            names = [self.ident()]
            while self.at_op(","):
                self.next()
                names.append(self.ident())
            self.constraint()
            for nm in names:
                self.declare(nm, "type")
                count += 1
            if self.at_op(","):
                self.next()
                continue
            break
        self.expect_op("]")
        return count

    def constraint(self):
        # union of ~T | T
        while True:
            if self.at_op("~"):
                self.next()
            self.type_()
            if self.at_op("|"):
                self.next()
                continue
            break

    def skip_brackets(self, i):
        """Index just past the ] matching the [ at token index i."""
        depth = 0
        n = len(self.toks)
        while i < n:
            t = self.toks[i]
            if t.kind == "OP":
                if t.val in ("[", "(", "{"):
                    depth += 1
                elif t.val in ("]", ")", "}"):
                    depth -= 1
                    if depth == 0:
                        return i + 1
            elif t.kind == "EOF":
                return i
            i += 1
        return i

    def tok_starts_type(self, t):
        if t.kind == "IDENT":
            return True
        if t.kind == "OP" and t.val in ("*", "[", "(", "<-"):
            return True
        if t.kind == "KEYWORD" and t.val in ("func", "map", "chan", "struct", "interface"):
            return True
        return False

    def params(self, declare):
        """Parse a parameter list. Returns (count, variadic)."""
        self.expect_op("(")
        items = []  # (kind, nametok)  kind: named | maybe | type
        variadic = False
        while not self.at_op(")"):
            if self.at_op("..."):
                self.next()
                variadic = True
                self.type_()
                items.append(("type", None))
            elif self.tok.kind == "IDENT":
                nt = self.tok
                nxt = self.toks[self.p + 1]
                if nxt.kind == "OP" and nxt.val in (",", ")"):
                    self.next()
                    items.append(("maybe", nt))
                elif nxt.kind == "OP" and nxt.val == ".":
                    self.type_()  # qualified type, unnamed parameter
                    items.append(("type", None))
                elif nxt.kind == "OP" and nxt.val == "...":
                    self.next()
                    self.next()
                    variadic = True
                    self.type_()
                    items.append(("named", nt))
                elif nxt.kind == "OP" and nxt.val == "[":
                    after = self.skip_brackets(self.p + 1)
                    inner = self.toks[self.p + 2]
                    if (inner.kind == "OP" and inner.val in ("]", "...")) or inner.kind == "INT" or self.tok_starts_type(self.toks[after]):
                        # name []T / name [N]T
                        self.next()
                        self.type_()
                        items.append(("named", nt))
                    else:
                        # generic instance T[X], unnamed
                        self.type_()
                        items.append(("type", None))
                else:
                    self.next()
                    self.type_()
                    items.append(("named", nt))
            else:
                self.type_()
                items.append(("type", None))
            if self.at_op(","):
                self.next()
                continue
            break
        self.expect_op(")")
        named = any(k == "named" for k, _ in items)
        if named:
            if declare:
                for k, t in items:
                    if t is not None:
                        self.declare(t, "param")
        else:
            for k, t in items:
                if k == "maybe":
                    self.use(t)
        return len(items), variadic

    def receiver(self):
        """(name *T[P, Q]) — declares name and the receiver type parameters."""
        self.expect_op("(")
        name = None
        if self.tok.kind == "IDENT":
            nxt = self.toks[self.p + 1]
            if not (nxt.kind == "OP" and nxt.val in (")", "[", ".")):
                name = self.tok
                self.next()
        if self.at_op("*"):
            self.next()
        tname = self.ident()
        self.use(tname)
        if self.at_op("["):
            self.next()
            while not self.at_op("]"):
                tp = self.ident()
                self.declare(tp, "type")
                if self.at_op(","):
                    self.next()
                    continue
                break
            self.expect_op("]")
        self.expect_op(")")
        if name is not None:
            self.declare(name, "param")

    def result(self, declare):
        if self.at_op("("):
            self.params(declare)
        elif self.starts_type():
            self.type_()

    def starts_type(self):
        t = self.tok
        if t.kind == "IDENT":
            return True
        if t.kind == "OP" and t.val in ("*", "[", "(", "<-"):
            return True
        if t.kind == "KEYWORD" and t.val in ("func", "map", "chan", "struct", "interface"):
            return True
        return False

    def gen_decl(self, top):
        kw = self.tok.val
        self.next()
        if self.at_op("("):
            self.next()
            iota_prev = None
            while not self.at_op(")"):
                iota_prev = self.spec(kw, top, iota_prev)
                self.expect_semi(kw + " declaration")
            self.next()
        else:
            self.spec(kw, top, None)

    def spec(self, kw, top, prev):
        if kw == "type":
            name = self.ident()
            if top:
                self.add_pkg(name, "type")
            else:
                self.declare(name, "type")
            if self.at_op("["):
                # generic type decl or array type
                save = self.p
                self.next()
                if self.tok.kind == "IDENT":
                    # type params like [T any]
                    self.p = save
                    self.tok = self.toks[self.p]
                    self.open_scope("tparams")
                    self.type_params()
                    if self.at_op("="):
                        self.next()
                    self.type_()
                    self.scope_discard()
                    return None
                self.p = save
                self.tok = self.toks[self.p]
            if self.at_op("="):
                self.next()
            self.type_()
            return None
        # var / const
        names = [self.ident()]
        while self.at_op(","):
            self.next()
            names.append(self.ident())
        has_type = False
        if not self.at_op("=") and not self.at_op(";") and not self.at_op(")"):
            self.type_()
            has_type = True
        nvals = 0
        if self.at_op("="):
            self.next()
            vals = self.expr_list()
            nvals = len(vals)
            self.fmt_list(vals, 1)
        if top:
            for nm in names:
                self.add_pkg(nm, kw)
        else:
            for nm in names:
                self.declare(nm, "var" if kw == "var" else "const")
        return names

    def scope_discard(self):
        self.scope = self.scope.parent

    # ----------------------------------------------------------------- types
    def type_(self):
        t = self.tok
        if t.kind == "IDENT":
            self.next()
            self.use(t)
            if self.at_op("."):
                self.next()
                self.ident()
            if self.at_op("["):
                self.type_args()
            return
        if t.kind == "OP":
            if t.val == "*":
                self.next()
                self.type_()
                return
            if t.val == "(":
                self.next()
                self.type_()
                self.expect_op(")")
                return
            if t.val == "[":
                self.next()
                if self.at_op("]"):
                    self.next()
                    self.type_()
                    return
                if self.at_op("..."):
                    self.next()
                else:
                    self.expr_lev += 1
                    self.expr()
                    self.expr_lev -= 1
                self.expect_op("]")
                self.type_()
                return
            if t.val == "<-":
                self.next()
                if not self.at_kw("chan"):
                    self.error("expected chan")
                self.next()
                self.type_()
                return
        if t.kind == "KEYWORD":
            if t.val == "map":
                self.next()
                self.expect_op("[")
                self.type_()
                self.expect_op("]")
                self.type_()
                return
            if t.val == "chan":
                self.next()
                if self.at_op("<-"):
                    self.next()
                self.type_()
                return
            if t.val == "func":
                self.next()
                self.open_scope("functype")
                self.params(declare=False)
                self.result(declare=False)
                self.scope_discard()
                return
            if t.val == "struct":
                self.struct_type()
                return
            if t.val == "interface":
                self.interface_type()
                return
        self.error("syntax error: unexpected %s, expected type" % (t.val if not t.auto else "newline"))

    def type_args(self):
        self.expect_op("[")
        self.expr_lev += 1
        while not self.at_op("]"):
            self.type_()
            if self.at_op(","):
                self.next()
                continue
            break
        self.expr_lev -= 1
        self.expect_op("]")

    def struct_type(self):
        self.next()  # struct
        self.expect_op("{")
        while not self.at_op("}"):
            if self.at_op("*"):
                self.next()
                self.type_()
            elif self.tok.kind == "IDENT":
                first = self.tok
                self.next()
                if self.at_op(",") :
                    while self.at_op(","):
                        self.next()
                        self.ident()
                    self.type_()
                elif self.at_op(";") or self.at_op("}") or self.tok.kind == "STRING":
                    self.use(first)  # embedded type
                elif self.at_op("."):
                    self.next()
                    self.ident()  # embedded qualified
                    self.use(first)
                else:
                    self.type_()
            else:
                self.error("syntax error: unexpected %s in struct type" % self.tok.val)
            if self.tok.kind == "STRING":
                self.next()  # tag
            self.expect_semi("struct field")
        self.next()

    def interface_type(self):
        self.next()
        self.expect_op("{")
        while not self.at_op("}"):
            if self.tok.kind == "IDENT":
                first = self.tok
                self.next()
                if self.at_op("("):
                    self.open_scope("functype")
                    self.params(declare=False)
                    self.result(declare=False)
                    self.scope_discard()
                elif self.at_op("."):
                    self.next()
                    self.ident()
                    self.use(first)
                    while self.at_op("|"):
                        self.next()
                        self.constraint()
                else:
                    self.use(first)
                    if self.at_op("["):
                        self.type_args()
                    while self.at_op("|"):
                        self.next()
                        if self.at_op("~"):
                            self.next()
                        self.type_()
            else:
                self.constraint()
            self.expect_semi("interface element")
        self.next()

    # ------------------------------------------------------------ statements
    def block_body(self, new_scope=True):
        self.expect_op("{")
        if new_scope:
            self.open_scope()
        save = self.expr_lev
        self.expr_lev = 0
        self.stmt_list()
        self.expr_lev = save
        self.expect_op("}")
        if new_scope:
            self.close_scope()

    def stmt_list(self):
        while not self.at_op("}") and self.tok.kind != "EOF" and not self.at_kw("case") and not self.at_kw("default"):
            self.stmt()

    def stmt(self):
        t = self.tok
        if t.kind == "OP" and t.val == ";":
            self.next()
            return
        if t.kind == "KEYWORD":
            v = t.val
            if v in ("var", "const", "type"):
                self.gen_decl(top=False)
                self.expect_semi()
                return
            if v == "return":
                self.next()
                if not self.at_op(";") and not self.at_op("}"):
                    self.fmt_list(self.expr_list(), 1)
                self.expect_semi()
                return
            if v in ("break", "continue", "goto"):
                self.next()
                if self.tok.kind == "IDENT":
                    self.next()
                self.expect_semi()
                return
            if v == "fallthrough":
                self.next()
                self.expect_semi()
                return
            if v == "go" or v == "defer":
                self.next()
                self.fmt_walk(self.expr(), 1)
                self.expect_semi()
                return
            if v == "if":
                self.if_stmt()
                self.expect_semi()
                return
            if v == "for":
                self.for_stmt()
                self.expect_semi()
                return
            if v == "switch":
                self.switch_stmt()
                self.expect_semi()
                return
            if v == "select":
                self.select_stmt()
                self.expect_semi()
                return
            if v == "func":
                # func literal expression statement
                self.simple_stmt()
                self.expect_semi()
                return
        if t.kind == "OP" and t.val == "{":
            self.block_body()
            self.expect_semi()
            return
        # labeled statement?
        if t.kind == "IDENT" and self.toks[self.p + 1].kind == "OP" and self.toks[self.p + 1].val == ":" :
            self.next()
            self.next()
            if self.at_op("}"):
                return
            self.stmt()
            return
        self.simple_stmt()
        self.expect_semi()

    def simple_stmt(self, allow_range=False, header=False):
        """Parse a simple statement. Returns ('range', ...) or kind."""
        if allow_range and self.at_kw("range"):
            self.next()
            x = self.expr()
            self.fmt_walk(x, 1)
            return "range", []
        lhs_start = self.p
        lhs = self.expr_list(lhs=True)
        t = self.tok
        if t.kind == "OP":
            if t.val == ":=":
                self.next()
                if allow_range and self.at_kw("range"):
                    self.next()
                    x = self.expr()
                    self.fmt_walk(x, 1)
                    return "range_define", lhs
                rhs = self.expr_list()
                d = 2 if len(lhs) > 1 and len(rhs) > 1 else 1
                self.fmt_list(lhs, d)
                self.fmt_list(rhs, d)
                self.define(lhs, t)
                return "define", lhs
            if t.val in ("=", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<=", ">>=", "&^="):
                self.next()
                if allow_range and t.val == "=" and self.at_kw("range"):
                    self.next()
                    x = self.expr()
                    self.fmt_walk(x, 1)
                    self.assign_uses(lhs)
                    return "range", lhs
                rhs = self.expr_list()
                d = 2 if len(lhs) > 1 and len(rhs) > 1 else 1
                self.fmt_list(lhs, d)
                self.fmt_list(rhs, d)
                self.assign_uses(lhs)
                return "assign", lhs
            if t.val in ("++", "--"):
                self.next()
                self.fmt_list(lhs, 2)
                self.assign_uses(lhs)
                return "incdec", lhs
            if t.val == "<-":
                self.next()
                v = self.expr()
                self.fmt_list(lhs, 1)
                self.fmt_walk(v, 1)
                self.mark_uses(lhs)
                return "send", lhs
        self.fmt_list(lhs, 1)
        self.mark_uses(lhs)
        return "expr", lhs

    # lhs expression tracking: expr_list(lhs=True) returns Expr nodes whose
    # identifier uses have NOT been registered yet (we defer so that ':=' can
    # declare them and '=' can treat them as assignment-only).
    def define(self, lhs, optok):
        new = 0
        for e in lhs:
            if e.kind != "ident":
                self.error("non-name on left side of :=", optok)
            name = e.name
            if name == "_":
                continue
            prev = self.scope.names.get(name) if self.scope else None
            if prev is None:
                new += 1
                self.declare(e.tok, "var")
            else:
                # re-assignment of an existing variable in the same scope
                pass
        if new == 0:
            self.locals_report.append((optok.line, optok.col, "no new variables on left side of :="))

    def assign_uses(self, lhs):
        for e in lhs:
            if e.kind == "ident":
                self.use(e.tok, assign_only=True)
            else:
                self.replay_uses(e)

    def mark_uses(self, lhs):
        for e in lhs:
            self.replay_uses(e)

    def replay_uses(self, e):
        # The deferred identifier uses recorded on the node.
        if e.kind == "ident":
            self.use(e.tok)
            return
        for t in getattr(e, "deferred", None) or ():
            self.use(t)

    def if_stmt(self):
        self.next()  # if
        self.open_scope()
        self.header()
        self.block_body()
        if self.at_kw("else"):
            self.next()
            if self.at_kw("if"):
                self.if_stmt()
            elif self.at_op("{"):
                self.block_body()
            else:
                self.error("else must be followed by if or statement block")
        self.close_scope()

    def header(self, is_for=False, is_switch=False):
        save = self.expr_lev
        self.expr_lev = -1
        result = None
        if not self.at_op("{"):
            if not self.at_op(";"):
                result = self.simple_stmt(allow_range=is_for)
            if self.at_op(";"):
                if self.tok.auto and not self.at_op("{"):
                    pass
                self.next()
                if is_for:
                    # for init; cond; post
                    if not self.at_op(";"):
                        self.expr()
                    self.expect_op(";")
                    if not self.at_op("{"):
                        self.simple_stmt()
                elif not self.at_op("{"):
                    result = self.simple_stmt()
        self.expr_lev = save
        return result

    def for_stmt(self):
        self.next()
        self.open_scope()
        save = self.expr_lev
        self.expr_lev = -1
        if not self.at_op("{"):
            if self.at_op(";"):
                # for ; cond ; post
                self.next()
                if not self.at_op(";"):
                    self.fmt_walk(self.expr(), 1)
                self.expect_op(";")
                if not self.at_op("{"):
                    self.simple_stmt()
            else:
                kind, lhs = self.simple_stmt(allow_range=True)
                if kind in ("range", "range_define"):
                    if kind == "range_define":
                        self.define(lhs, self.tok)
                        # range variables count as used if the loop uses them;
                        # gc does report unused range vars declared with :=
                elif self.at_op(";"):
                    self.next()
                    if not self.at_op(";"):
                        self.fmt_walk(self.expr(), 1)
                    self.expect_op(";")
                    if not self.at_op("{"):
                        self.simple_stmt()
        self.expr_lev = save
        self.block_body()
        self.close_scope()

    def switch_stmt(self):
        self.next()
        self.open_scope()
        save = self.expr_lev
        self.expr_lev = -1
        type_switch_var = None
        if not self.at_op("{"):
            if not self.at_op(";"):
                type_switch_var = self.switch_header_stmt()
            if self.at_op(";"):
                self.next()
                if not self.at_op("{"):
                    type_switch_var = self.switch_header_stmt()
        self.expr_lev = save
        self.expect_op("{")
        while not self.at_op("}"):
            if self.at_kw("case"):
                self.next()
                self.case_list()
            elif self.at_kw("default"):
                self.next()
            else:
                self.error("syntax error: unexpected %s, expected case or default or }" % self.tok.val)
            self.expect_op(":")
            self.open_scope()
            if type_switch_var is not None:
                info = self.declare(type_switch_var, "tsvar")
            self.stmt_list()
            self.close_scope()
        self.next()
        self.close_scope()

    def switch_header_stmt(self):
        # handles `x := y.(type)` (returns the name token) and plain stmts
        if self.tok.kind == "IDENT" and self.toks[self.p + 1].kind == "OP" and self.toks[self.p + 1].val == ":=":
            save = self.p
            name = self.tok
            self.next()
            self.next()
            e = self.expr()
            if getattr(e, "is_type_switch", False):
                return name
            # ordinary := in switch init
            self.p = save
            self.tok = self.toks[self.p]
            self.simple_stmt()
            return None
        self.simple_stmt()
        return None

    def case_list(self):
        while True:
            self.fmt_walk(self.expr_or_type(), 1)
            if self.at_op(","):
                self.next()
                continue
            break

    def expr_or_type(self):
        # Types that can appear in expression position ([]T, map, chan, func,
        # struct, interface, *T) are operands, so composite literals and
        # conversions after them are handled by primary().
        return self.expr()

    def select_stmt(self):
        self.next()
        self.expect_op("{")
        while not self.at_op("}"):
            self.open_scope()
            if self.at_kw("case"):
                self.next()
                save = self.expr_lev
                self.simple_stmt()
            elif self.at_kw("default"):
                self.next()
            else:
                self.error("syntax error: expected case or default")
            self.expect_op(":")
            self.stmt_list()
            self.close_scope()
        self.next()

    # ----------------------------------------------------------- expressions
    def expr_list(self, lhs=False):
        out = [self.expr(lhs=lhs)]
        while self.at_op(","):
            self.next()
            out.append(self.expr(lhs=lhs))
        return out

    BINPREC = {
        "||": 1, "&&": 2,
        "==": 3, "!=": 3, "<": 3, "<=": 3, ">": 3, ">=": 3,
        "+": 4, "-": 4, "|": 4, "^": 4,
        "*": 5, "/": 5, "%": 5, "<<": 5, ">>": 5, "&": 5, "&^": 5,
    }

    def expr(self, lhs=False):
        return self.binary(1, lhs)

    def binary(self, prec, lhs):
        x = self.unary(lhs)
        while self.tok.kind == "OP" and self.BINPREC.get(self.tok.val, 0) >= prec:
            op = self.tok.val
            op_i = self.p
            oprec = self.BINPREC[op]
            self.next()
            if lhs:
                self.flush(x)
                lhs = False
            y = self.binary(oprec + 1, False)
            e = Expr("binary")
            e.op = op
            e.op_i = op_i
            e.x = x
            e.y = y
            x = e
        return x

    def flush(self, e):
        """Register deferred uses of an lhs-mode expression immediately."""
        if e is None:
            return
        if e.kind == "ident":
            self.use(e.tok)
        else:
            for t in getattr(e, "deferred", None) or ():
                self.use(t)
            e.deferred = []

    def unary(self, lhs=False):
        t = self.tok
        if t.kind == "OP" and t.val in ("+", "-", "!", "^", "&", "<-", "*", "~"):
            self.next()
            if t.val == "<-" and self.at_kw("chan"):
                self.next()
                self.type_()
                return Expr("type")
            x = self.unary(False)
            if t.val == "*":
                e = Expr("star", x=x)
                return e
            e = Expr("unary", x=x)
            e.op = t.val
            return e
        return self.primary(lhs)

    def operand(self, lhs):
        t = self.tok
        if t.kind == "IDENT":
            self.next()
            e = Expr("ident", name=t.val, tok=t)
            if not lhs:
                self.use(t)
            return e
        if t.kind in ("INT", "STRING", "CHAR"):
            self.next()
            return Expr("lit")
        if t.kind == "OP" and t.val == "(":
            self.next()
            self.expr_lev += 1
            x = self.expr_or_type()
            self.expr_lev -= 1
            self.expect_op(")")
            e = Expr("paren", x=x)
            return e
        if t.kind == "KEYWORD":
            if t.val == "func":
                self.next()
                self.open_scope("func")
                self.fn_depth += 1
                self.params(declare=True)
                self.result(declare=True)
                if self.at_op("{"):
                    self.block_body(new_scope=False)
                    self.fn_depth -= 1
                    self.close_scope()
                    return Expr("func")
                self.fn_depth -= 1
                self.scope_discard()
                e = Expr("type")
                e.is_type = True
                return e
            if t.val in ("map", "chan", "struct", "interface"):
                self.type_()
                e = Expr("type")
                e.is_type = True
                return e
        if t.kind == "OP" and t.val == "[":
            self.type_()
            e = Expr("type")
            e.is_type = True
            return e
        what = t.val if not t.auto else "newline"
        if t.kind == "EOF":
            what = "EOF"
        self.error("syntax error: unexpected %s, expected expression" % what)

    def primary(self, lhs=False):
        x = self.operand(lhs)
        while True:
            t = self.tok
            if t.kind != "OP":
                break
            if t.val == ".":
                self.next()
                if self.at_op("("):
                    self.next()
                    if self.at_kw("type"):
                        self.next()
                        self.expect_op(")")
                        if lhs:
                            self.flush(x)
                            lhs = False
                        e = Expr("typeswitch", x=x)
                        e.is_type_switch = True
                        x = e
                        continue
                    self.type_()
                    self.expect_op(")")
                    if lhs:
                        self.flush(x)
                        lhs = False
                    x = Expr("assert", x=x)
                    continue
                sel = self.ident()
                if lhs:
                    # x.f on the lhs uses x
                    self.flush(x)
                    lhs = False
                tname = None
                if x.kind == "ident":
                    tname = x.name + "." + sel.val
                x = Expr("selector", name=tname, x=x)
                continue
            if t.val == "[":
                self.next()
                if lhs:
                    self.flush(x)
                    lhs = False
                self.expr_lev += 1
                parts = []
                kind = "index"
                if self.at_op(":"):
                    kind = "slice"
                    parts.append(None)
                    self.next()
                    if not self.at_op("]"):
                        parts.append(self.expr())
                        if self.at_op(":"):
                            self.next()
                            parts.append(self.expr())
                else:
                    parts.append(self.expr_or_type())
                    if self.at_op(":"):
                        kind = "slice"
                        self.next()
                        if not self.at_op("]"):
                            parts.append(self.expr())
                            if self.at_op(":"):
                                self.next()
                                parts.append(self.expr())
                    else:
                        while self.at_op(","):
                            self.next()
                            if self.at_op("]"):
                                break
                            parts.append(self.expr_or_type())
                self.expr_lev -= 1
                self.expect_op("]")
                nx = Expr(kind, name=None, x=x)
                nx.idx = parts
                if kind == "index" and x.kind in ("ident", "selector"):
                    nx.name = x.name
                    nx.is_type = True  # could be a generic instantiation
                x = nx
                continue
            if t.val == "(":
                self.next()
                if lhs:
                    self.flush(x)
                    lhs = False
                self.expr_lev += 1
                args = []
                ellipsis = False
                while not self.at_op(")"):
                    a = self.expr_or_type()
                    args.append(a)
                    if self.at_op("..."):
                        self.next()
                        ellipsis = True
                    if self.at_op(","):
                        self.next()
                        continue
                    break
                self.expr_lev -= 1
                if not self.at_op(")"):
                    what = self.tok.val if not self.tok.auto else "newline"
                    self.error("syntax error: unexpected %s in argument list; possibly missing comma or )" % what)
                self.next()
                nargs = len(args)
                if x.kind == "ident":
                    self.calls.append((x.name, nargs, t.line, ellipsis,
                                       nargs == 1 and args[0].kind == "call", self.lookup_local(x.name) is not None))
                e = Expr("call", x=x)
                e.args = args
                x = e
                continue
            if t.val == "{":
                if self.is_literal_type(x) and (self.expr_lev >= 0 or not self.is_type_name(x)):
                    if lhs:
                        self.flush(x)
                        lhs = False
                    tname = x.name if x.kind in ("ident", "selector") else None
                    elts = self.composite_lit(tname)
                    e = Expr("composite", x=x)
                    e.elts = elts
                    x = e
                    continue
                break
            break
        if lhs and x.kind != "ident":
            self.flush(x)
        return x

    def is_type_name(self, x):
        # go/parser: identifiers, selectors and index expressions only start a
        # composite literal when exprLev >= 0 (i.e. not in a control clause).
        return x.kind in ("ident", "selector", "index")

    def is_literal_type(self, x):
        if x.kind in ("ident", "selector"):
            return True
        if x.kind == "type":
            return True
        if x.kind == "index" and x.is_type:
            return True
        return False

    def composite_lit(self, tname):
        lbrace = self.expect_op("{")
        self.expr_lev += 1
        elts = []
        spans = []
        while not self.at_op("}"):
            first_i = self.p
            key, val, colon_i = self.element(tname)
            el = (key, val)
            last_i = self.p - 1
            elts.append(el)
            spans.append((first_i, last_i, el, colon_i))
            if self.at_op(","):
                self.next()
                continue
            if not self.at_op("}"):
                what = self.tok.val if not self.tok.auto else "newline"
                self.error("syntax error: unexpected %s in composite literal; possibly missing comma or }" % what)
        self.expr_lev -= 1
        self.expect_op("}")
        self.check_kv_alignment(lbrace, spans)
        return elts

    def check_kv_alignment(self, lbrace, spans):
        """go/printer exprList: keys of one-per-line key:value elements are
        aligned in sections (values start in one column)."""
        import math as _m
        if len(spans) < 2:
            return
        prev_line = lbrace.line
        size = 0
        lnsum = 0.0
        count = 0
        prev_break = -1
        if lbrace.line < self.toks[spans[0][0]].line:
            prev_break = 0
        sections = []
        cur = []
        for i, (fi, li, el, colon_i) in enumerate(spans):
            ft, lt = self.toks[fi], self.toks[li]
            start_line, end_line = ft.line, lt.line
            key, val = el
            is_pair = key is not None and colon_i is not None
            use_ff = True
            prev_size = size
            if start_line == end_line:
                if is_pair:
                    ct = self.toks[colon_i]
                    size = ct.col - ft.col  # key source width
                else:
                    size = (lt.col + len(lt.val)) - ft.col
                if size <= 0:
                    size = 1
            else:
                size = 0
            if prev_size > 0 and size > 0:
                if count == 0 or (prev_size <= 40 and size <= 40):
                    use_ff = False
                else:
                    geomean = _m.exp(lnsum / count)
                    ratio = size / geomean
                    use_ff = 2.5 * ratio <= 1 or 2.5 <= ratio
            needs_lb = 0 < prev_line < start_line
            if i > 0 and needs_lb:
                comment_between = any(ln in self.comment_only for ln in range(prev_line + 1, start_line))
                nbreaks = start_line - prev_line
                ff = use_ff or prev_break + 1 < i
                if ff or nbreaks > 1 or comment_between:
                    if cur:
                        sections.append(cur)
                    cur = []
                if ff or nbreaks > 1:
                    # linebreak() reports a formfeed as more than one break,
                    # which resets the key-size statistics.
                    lnsum = 0.0
                    count = 0
                prev_break = i
            if is_pair and size > 0 and needs_lb:
                cur.append((fi, colon_i))
            if size > 0:
                lnsum += _m.log(size)
                count += 1
            prev_line = end_line
        if cur:
            sections.append(cur)
        for sec in sections:
            width = max(self.toks[c].col + 1 - self.toks[f].col for f, c in sec)
            for f, c in sec:
                vt = self.toks[c + 1]
                ft = self.toks[f]
                if vt.line != ft.line:
                    continue
                want = ft.col + width + 1
                if vt.col != want:
                    self.fmt_report.append((vt.line, vt.col, "gofmt: key/value alignment, value should start at column %d" % want))

    def elt_value(self):
        if self.at_op("{"):
            e = Expr("composite", x=None)
            e.elts = self.composite_lit(None)
            return e
        return self.expr()

    def element(self, tname):
        # key: value | value ; keys may be bare field names.
        # Returns (key, value, colon_token_index).
        if self.at_op("{"):
            return (None, self.elt_value(), None)
        if self.tok.kind == "IDENT" and self.toks[self.p + 1].kind == "OP" and self.toks[self.p + 1].val == ":":
            key = self.tok
            colon = self.p + 1
            self.next()
            self.next()
            # Bare identifier key: a struct field name or a map key
            # expression. Resolve it only if it is a known local/import.
            if self.lookup_local(key.val) is not None:
                self.use(key)
            else:
                self.unresolved.append(("?key:" + key.val, key.line, key.col))
            if tname is not None:
                self.literal_checks.append((tname, key.val, key.line))
            return (Expr("ident", name=key.val, tok=key), self.elt_value(), colon)
        k = self.expr()
        if self.at_op(":"):
            colon = self.p
            self.next()
            return (k, self.elt_value(), colon)
        return (None, k, None)

    # ------------------------------------------------- gofmt spacing (binary)
    def diff_prec(self, e, prec):
        if e is None or e.kind != "binary" or self.BINPREC[e.op] != prec:
            return 1
        return 0

    def walk_binary(self, e):
        has4 = has5 = False
        max_problem = 0
        p = self.BINPREC[e.op]
        if p == 4:
            has4 = True
        elif p == 5:
            has5 = True
        l = e.x
        if l is not None and l.kind == "binary" and not (self.BINPREC[l.op] < p):
            h4, h5, mp = self.walk_binary(l)
            has4 = has4 or h4
            has5 = has5 or h5
            max_problem = max(max_problem, mp)
        r = e.y
        if r is not None:
            if r.kind == "binary":
                if not (self.BINPREC[r.op] <= p):
                    h4, h5, mp = self.walk_binary(r)
                    has4 = has4 or h4
                    has5 = has5 or h5
                    max_problem = max(max_problem, mp)
            elif r.kind == "star":
                if e.op == "/":
                    max_problem = 5
            elif r.kind == "unary":
                pair = e.op + getattr(r, "op", "")
                if pair in ("/*", "&&", "&^"):
                    max_problem = 5
                elif pair in ("++", "--"):
                    if max_problem < 4:
                        max_problem = 4
        return has4, has5, max_problem

    def cutoff(self, e, depth):
        has4, has5, mp = self.walk_binary(e)
        if mp > 0:
            return mp + 1
        if has4 and has5:
            if depth == 1:
                return 5
            return 4
        if depth == 1:
            return 6
        return 4

    def fmt_walk(self, e, depth):
        if e is None:
            return
        k = e.kind
        if k == "binary":
            if depth < 1:
                depth = 1
            prec = self.BINPREC[e.op]
            blank = prec < self.cutoff(e, depth)
            self.check_op_spacing(e.op_i, blank)
            self.fmt_walk(e.x, depth + self.diff_prec(e.x, prec))
            self.fmt_walk(e.y, depth + 1)
        elif k == "paren":
            if e.x is not None and e.x.kind == "paren":
                self.fmt_walk(e.x, depth)
            else:
                self.fmt_walk(e.x, max(1, depth - 1))
        elif k in ("unary", "selector", "assert", "typeswitch"):
            self.fmt_walk(e.x, depth)
        elif k == "star":
            self.fmt_walk(e.x, 1)
        elif k in ("index", "slice"):
            self.fmt_walk(e.x, 1)
            for part in getattr(e, "idx", None) or ():
                self.fmt_walk(part, depth + 1)
        elif k == "call":
            d = depth + 1 if len(e.args or ()) > 1 else depth
            self.fmt_walk(e.x, d)
            for a in e.args or ():
                self.fmt_walk(a, d)
        elif k == "composite":
            self.fmt_walk(e.x, depth)
            for key, val in getattr(e, "elts", None) or ():
                self.fmt_walk(key, 1)
                self.fmt_walk(val, 1)

    def check_op_spacing(self, op_i, blank):
        if op_i is None or op_i <= 0:
            return
        prev = self.toks[op_i - 1]
        op = self.toks[op_i]
        nxt = self.toks[op_i + 1]
        if prev.line != op.line or nxt.line != op.line:
            return
        if prev.kind == "STRING" and "\n" in prev.val:
            return
        left = op.col - (prev.col + len(prev.val))
        right = nxt.col - (op.col + len(op.val))
        want = 1 if blank else 0
        if left != want or right != want:
            self.fmt_report.append((op.line, op.col, "gofmt: want %s around %s" % ("spaces" if blank else "no spaces", op.val)))

    def fmt_list(self, exprs, depth):
        for e in exprs:
            self.fmt_walk(e, depth)



def whitespace_problems(src):
    """Leading-space indentation and trailing whitespace (gofmt removes both),
    outside raw string literals."""
    probs = []
    # find raw string spans to skip
    raw_lines = set()
    i = 0
    n = len(src)
    line = 1
    in_line_comment = False
    while i < n:
        c = src[i]
        if c == "\n":
            line += 1
            i += 1
            continue
        if src.startswith("//", i):
            j = src.find("\n", i)
            i = n if j < 0 else j
            continue
        if src.startswith("/*", i):
            j = src.find("*/", i + 2)
            j = n if j < 0 else j + 2
            line += src.count("\n", i, j)
            i = j
            continue
        if c == '"' or c == "'":
            q = c
            i += 1
            while i < n and src[i] != q and src[i] != "\n":
                if src[i] == "\\":
                    i += 1
                i += 1
            i += 1
            continue
        if c == "`":
            j = src.find("`", i + 1)
            j = n if j < 0 else j
            start = line
            nl = src.count("\n", i, j)
            for k in range(start, start + nl + 1):
                raw_lines.add(k)
            line += nl
            i = j + 1
            continue
        i += 1
    all_lines = src.split("\n")
    for ln, text in enumerate(all_lines, 1):
        if ln in raw_lines:
            continue
        if ln >= 2 and ln < len(all_lines) and text.strip() == "" and all_lines[ln - 2].strip() == "" and (ln - 1) not in raw_lines:
            probs.append((ln, 1, "gofmt: more than one blank line"))
        if text.rstrip("\r") != text.rstrip("\r").rstrip(" \t"):
            probs.append((ln, len(text), "gofmt: trailing whitespace"))
        stripped = text.lstrip("\t")
        if stripped.startswith(" ") and stripped.strip() != "":
            # a space after the tab indentation; allowed only inside block comments
            if not stripped.lstrip().startswith("*"):
                probs.append((ln, 1, "gofmt: indentation must use tabs"))
    return probs


def import_name(path):
    parts = path.split("/")
    last = parts[-1]
    if re.fullmatch(r"v\d+", last) and len(parts) > 1:
        last = parts[-2]
    if last.startswith("go-"):
        last = last[3:]
    last = last.replace("-", "_").replace(".", "_")
    known = {"go-sdl2": "sdl", "yaml_v3": "yaml", "yaml_v2": "yaml"}
    return known.get(last, last)


LITERAL_FIELDS = {
    "sdl.Rect": {"X", "Y", "W", "H"},
    "sdl.FRect": {"X", "Y", "W", "H"},
    "sdl.Color": {"R", "G", "B", "A"},
    "sdl.Point": {"X", "Y"},
    "sdl.FPoint": {"X", "Y"},
}


def vendor_package_name(root, path):
    d = os.path.join(root, "vendor", path)
    if not os.path.isdir(d):
        return None
    for fn in sorted(os.listdir(d)):
        if fn.endswith(".go") and not fn.endswith("_test.go"):
            try:
                with open(os.path.join(d, fn), encoding="utf-8", errors="replace") as f:
                    for line in f:
                        m = re.match(r"\s*package\s+(\w+)", line)
                        if m:
                            return m.group(1)
            except OSError:
                pass
    return None



KNOWN_OS = {"aix", "android", "darwin", "dragonfly", "freebsd", "hurd", "illumos", "ios", "js",
            "linux", "nacl", "netbsd", "openbsd", "plan9", "solaris", "wasip1", "windows", "zos"}
KNOWN_ARCH = {"386", "amd64", "amd64p32", "arm", "armbe", "arm64", "arm64be", "loong64", "mips",
              "mipsle", "mips64", "mips64le", "ppc", "ppc64", "ppc64le", "riscv", "riscv64", "s390",
              "s390x", "sparc", "sparc64", "wasm"}


def build_tags(goos, goarch, cgo=True):
    tags = {goos, goarch, "gc"}
    if goos in ("linux", "darwin", "freebsd", "netbsd", "openbsd", "android", "ios", "solaris", "aix", "dragonfly", "illumos"):
        tags.add("unix")
    if cgo:
        tags.add("cgo")
    for minor in range(1, 30):
        tags.add("go1.%d" % minor)
    return tags


def eval_constraint(expr, tags):
    toks = re.findall(r"\|\||&&|!|\(|\)|[A-Za-z0-9_.]+", expr)
    out = []
    for t in toks:
        if t == "||":
            out.append(" or ")
        elif t == "&&":
            out.append(" and ")
        elif t == "!":
            out.append(" not ")
        elif t in ("(", ")"):
            out.append(t)
        else:
            out.append(" True " if t in tags else " False ")
    try:
        return bool(eval("".join(out)))
    except Exception:
        return True


def file_matches(fn, src, goos, goarch):
    base = fn[:-3]
    if base.endswith("_test"):
        base = base[:-5]
    parts = base.split("_")
    if len(parts) >= 3 and parts[-2] in KNOWN_OS and parts[-1] in KNOWN_ARCH:
        if parts[-2] != goos or parts[-1] != goarch:
            return False
    elif len(parts) >= 2:
        if parts[-1] in KNOWN_OS and parts[-1] != goos:
            return False
        if parts[-1] in KNOWN_ARCH and parts[-1] != goarch:
            return False
    tags = build_tags(goos, goarch)
    for line in src.split("\n")[:40]:
        s = line.strip()
        if s.startswith("//go:build "):
            return eval_constraint(s[len("//go:build "):], tags)
        if s.startswith("package "):
            break
    return True


def check_package(root, files, report_files=None, include_tests=False, goos='linux', goarch='arm64'):
    """Parse every file; return list of (file, line, col, msg)."""
    errors = []
    parsers = {}
    for fn in files:
        path = os.path.join(root, fn)
        with open(path, encoding="utf-8", errors="replace") as f:
            src = f.read()
        if not file_matches(fn, src, goos, goarch):
            continue
        try:
            toks = tokenize(src)
            p = Parser(toks, fn, None)
            p.parse_file()
        except GoSyntaxError as e:
            errors.append((fn, e.line, e.col, e.msg))
            continue
        except RecursionError:
            errors.append((fn, 0, 0, "parser recursion limit"))
            continue
        parsers[fn] = p
        if CHECK_FMT:
            for (line, col, msg) in whitespace_problems(src):
                p.fmt_report.append((line, col, msg))
    # Package scope.
    pkg = {}
    for fn, p in parsers.items():
        for name, entries in p.pkg_decls.items():
            pkg.setdefault(name, []).extend((fn,) + e for e in entries)
    # Duplicate package-level declarations (excluding methods, which are not in pkg_decls)
    for name, entries in pkg.items():
        if len(entries) > 1:
            files_ = sorted({e[0] for e in entries})
            first = entries[0]
            for e in entries[1:]:
                errors.append((e[0], e[2], 0, "%s redeclared in this package (also %s:%d)" % (name, first[0], first[2])))
    funcs = {}
    for name, entries in pkg.items():
        for e in entries:
            if e[1] == "func":
                funcs[name] = e[3]
    for fn, p in parsers.items():
        for (line, col, msg) in p.locals_report:
            errors.append((fn, line, col, msg))
        if CHECK_FMT:
            for (line, col, msg) in p.fmt_report:
                errors.append((fn, line, col, msg))
        for name, line, col in p.unresolved:
            key = False
            if name.startswith("?key:"):
                name = name[5:]
                key = True
            if name in pkg or name in UNIVERSE or name in p.file_imports:
                if name in p.file_imports:
                    p.file_imports[name].used = True
                continue
            if key:
                continue
            errors.append((fn, line, col, "undefined: %s" % name))
        for name, info in p.file_imports.items():
            if not info.used:
                errors.append((fn, info.line, 0, "\"%s\" imported and not used" % info.kind.split(":", 1)[1]))
        for (name, nargs, line, ellipsis, single_call, shadowed) in p.calls:
            if shadowed or name not in funcs:
                continue
            if name in p.file_imports:
                continue
            want, variadic = funcs[name]
            if single_call:
                continue
            if variadic:
                if ellipsis:
                    if nargs != want:
                        errors.append((fn, line, 0, "call to %s with ... has %d args, want %d" % (name, nargs, want)))
                elif nargs < want - 1:
                    errors.append((fn, line, 0, "not enough arguments in call to %s: have %d, want >= %d" % (name, nargs, want - 1)))
            else:
                if nargs != want:
                    errors.append((fn, line, 0, "wrong argument count in call to %s: have %d, want %d" % (name, nargs, want)))
        for tname, key, line in p.literal_checks:
            fields = LITERAL_FIELDS.get(tname)
            if fields is not None and key not in fields:
                errors.append((fn, line, 0, "unknown field %s in struct literal of type %s" % (key, tname)))
    if report_files is not None:
        rs = set(report_files)
        errors = [e for e in errors if e[0] in rs]
    errors.sort()
    return errors


CHECK_FMT = True


def main(argv):
    global CHECK_FMT
    include_tests = False
    args = []
    for a in argv[1:]:
        if a == "--tests":
            include_tests = True
        elif a == "--nofmt":
            CHECK_FMT = False
        else:
            args.append(a)
    if not args:
        print(__doc__)
        return 2
    root = args[0]
    report = args[1:] or None
    names = sorted(fn for fn in os.listdir(root) if fn.endswith(".go"))
    prod = [fn for fn in names if not fn.endswith("_test.go")]
    tests = [fn for fn in names if fn.endswith("_test.go")]
    sys.setrecursionlimit(20000)
    errs = []
    seen = set()
    for goos, goarch in (("linux", "arm64"), ("windows", "amd64")):
        batch = check_package(root, prod, report_files=report, goos=goos, goarch=goarch)
        if include_tests:
            batch += [e for e in check_package(root, prod + tests, report_files=report, goos=goos, goarch=goarch)
                      if e[0].endswith("_test.go")]
        for e in batch:
            if e not in seen:
                seen.add(e)
                errs.append(e + (goos,))
    errs = [e[:4] for e in sorted(errs)]
    for fn, line, col, msg in errs:
        print("%s:%d:%d: %s" % (fn, line, col, msg))
    print("gocheck: %d problem(s)" % len(errs), file=sys.stderr)
    return 1 if errs else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
