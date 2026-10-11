#!/usr/bin/env python3
"""PreToolUse hook on Bash: run a GNU-style sed command in its BSD (macOS)
spelling, or deny it with the reason when there is no exact one.

GNU regex escapes (\\b \\< \\> \\w \\W \\s \\S, and in basic mode \\+ \\? \\|)
are read by BSD sed as literal characters, so a substitution using them
matches nothing and exits 0 without changing any file. Agents then run the
tests on unchanged code, see them pass, and report the edit done: on the
2026-10-10 benchmark that silent no-op cost the only lost task and several
wasted turns, in both the native and the prism arm. BSD `-i` also takes the
next argument as a backup suffix, so `-i -e ...` edits the file and leaves a
stray `<file>-e` copy, and GNU-style `-i 's/...'` fails.

Denying cost the agent one call to rewrite the command, so where the BSD
spelling is exact (\\w -> [[:alnum:]_], \\bfoo\\b -> [[:<:]]foo[[:>:]], \\+ ->
\\{1,\\}, -i -> -i '', basic \\| -> the -E form) the hook rewrites the sed
arguments in place, leaves the rest of the command byte-for-byte, and tells
the agent what ran. It sets no permission decision, so the rewritten command
goes through the user's normal permission flow. Checked 2026-10-10 against
579 benchmark sed scripts: GNU sed on the original and BSD sed on the
rewrite gave byte-identical output on a 10k-line sample.

Never blocks when in doubt:
  - does nothing unless the command mentions sed and the installed sed is BSD
    (`sed --version` fails; GNU sed answers it);
  - unparseable commands (heredocs, unbalanced quotes) are allowed;
  - only the sed script arguments are inspected, not file names or other
    commands in the pipeline.
Installed and removed with the read guard (`prism init --read-guard` /
`--no-read-guard`).
"""
import json
import re
import shlex
import subprocess
import sys

GNU_ESCAPES = re.compile(r"\\[bBwWsS<>]")
GNU_BRE_OPERATORS = re.compile(r"\\[+?|]")
SEPARATORS = {";", "&&", "||", "|", "&", "\n", "(", ")"}


def is_bsd_sed() -> bool:
    try:
        r = subprocess.run(["sed", "--version"], capture_output=True, text=True, timeout=5)
    except (OSError, subprocess.SubprocessError):
        return False
    return r.returncode != 0


def sed_invocations(command: str):
    """Yield the argument lists of each `sed` in the command line."""
    lexer = shlex.shlex(command.replace("\\\n", " ").replace("\n", " ; "), posix=True, punctuation_chars=";&|()")
    lexer.whitespace_split = True
    tokens = list(lexer)
    current = []
    for tok in tokens + [";"]:
        if tok in SEPARATORS or (tok and set(tok) <= set(";&|()")):
            # sed may follow a wrapper: `xargs sed ...`, `env X=1 sed ...`,
            # `find . -exec sed ... {} ;`.
            for j, word in enumerate(current):
                if word == "sed" or word.endswith("/sed"):
                    yield current[j + 1:]
                    break
            current = []
        else:
            current.append(tok)


def problems(args) -> list:
    ere = False
    scripts = []
    found = []
    i = 0
    while i < len(args):
        a = args[i]
        if a in ("-E", "-r") or (a.startswith("-") and not a.startswith("--") and len(a) > 1
                                  and set(a[1:]) <= set("Enrsu") and ("E" in a or "r" in a)):
            ere = True
        if a == "-i":
            nxt = args[i + 1] if i + 1 < len(args) else ""
            if nxt.startswith("-"):
                # BSD takes the flag itself as the suffix: `-i -e s/a/b/ f`
                # edits f and also writes f-e; the flag is not applied.
                found.append(f"BSD sed reads the argument after -i as a backup suffix, so `-i {nxt}` "
                             f"edits the file and also writes a `<file>{nxt}` copy into the tree, "
                             f"and {nxt} is not applied as an option. `-i ''` means in place with "
                             "no backup.")
                i += 1  # the flag after it is still parsed as an option below
                continue
            if nxt and ("/" in nxt or nxt[:1] in "sy"):
                found.append("BSD sed reads the argument after -i as a backup suffix, so the sed "
                             "script there is taken as the suffix and the command fails. "
                             "`-i ''` means in place with no backup.")
                scripts.append(nxt)
            i += 2
            continue
        if a in ("-e", "--expression"):
            if i + 1 < len(args):
                scripts.append(args[i + 1])
            i += 2
            continue
        if a in ("-f", "--file", "-l"):
            i += 2
            continue
        if a.startswith("-") and len(a) > 1:
            i += 1
            continue
        if not scripts:
            scripts.append(a)  # first operand is the script when no -e was given
        i += 1
    for s in scripts:
        if GNU_ESCAPES.search(s) or (not ere and GNU_BRE_OPERATORS.search(s)):
            found.append("this system's sed is BSD (macOS) sed, which does not support GNU regex "
                         "escapes (\\b \\< \\> \\w \\s, and without -E \\+ \\? \\|). It reads them as "
                         "literal characters, so this pattern matches nothing and the command exits "
                         "successfully without changing any file. BSD sed spells word boundaries "
                         "[[:<:]] and [[:>:]]; -E enables + ? | without backslashes.")
            break
    return found



# --- Translation -------------------------------------------------------------
# A denied command costs the agent a call to rewrite it. Where the BSD spelling
# of the same command is exact, the hook rewrites it instead and tells the
# agent what ran. Anything it cannot translate exactly is denied as above.

WORD_CHARS = set("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_")
CLASS_FOR = {"w": "[[:alnum:]_]", "W": "[^[:alnum:]_]", "s": "[[:space:]]", "S": "[^[:space:]]",
             "<": "[[:<:]]", ">": "[[:>:]]"}


class Untranslatable(Exception):
    pass


def shell_words(command: str):
    """Split a command into (start, end, value, is_operator) with raw spans,
    so a word can be replaced in place without re-quoting the rest. Raises
    Untranslatable on shell syntax it does not model (heredocs, backticks,
    unbalanced quotes)."""
    out, i, n = [], 0, len(command)
    while i < n:
        ch = command[i]
        if ch in " \t" or command.startswith("\\\n", i):
            i += 2 if ch == "\\" else 1
            continue
        if ch == "\n" or ch in ";&|()":
            j = i + 1
            while j < n and command[j] in ";&|" and ch != "\n":
                j += 1
            if command[i:j].startswith("<<"):
                raise Untranslatable()
            out.append((i, j, command[i:j], True))
            i = j
            continue
        start, val = i, []
        while i < n and command[i] not in " \t\n;&|()":
            ch = command[i]
            if ch == "'":
                j = command.find("'", i + 1)
                if j < 0:
                    raise Untranslatable()
                val.append(command[i + 1:j])
                i = j + 1
            elif ch == '"':
                j, buf = i + 1, []
                while j < n and command[j] != '"':
                    if command[j] == "\\" and j + 1 < n and command[j + 1] in '$`"\\\n':
                        buf.append(command[j + 1])
                        j += 2
                        continue
                    buf.append(command[j])
                    j += 1
                if j >= n:
                    raise Untranslatable()
                val.append("".join(buf))
                i = j + 1
            elif ch == "\\":
                if i + 1 < n and command[i + 1] != "\n":
                    val.append(command[i + 1])
                i += 2
            elif ch == "`" or command.startswith("<<", i):
                raise Untranslatable()
            elif command.startswith("$(", i):
                depth, j = 1, i + 2
                while j < n and depth:
                    depth += {"(": 1, ")": -1}.get(command[j], 0)
                    j += 1
                if depth:
                    raise Untranslatable()
                val.append(command[i:j])
                i = j
            else:
                val.append(ch)
                i += 1
        out.append((start, i, "".join(val), False))
    return out


def _read_bracket(rx: str, i: int) -> int:
    """rx[i] == '['; return the index just past the matching ']'."""
    j = i + 1
    if j < len(rx) and rx[j] == "^":
        j += 1
    if j < len(rx) and rx[j] == "]":
        j += 1
    while j < len(rx):
        if rx.startswith("[:", j) or rx.startswith("[.", j) or rx.startswith("[=", j):
            k = rx.find(rx[j + 1] + "]", j + 2)
            if k < 0:
                raise Untranslatable()
            j = k + 2
            continue
        if rx[j] == "]":
            return j + 1
        j += 1
    raise Untranslatable()


BRE_TO_ERE = {"(": "(", ")": ")", "{": "{", "}": "}", "|": "|", "+": "+", "?": "?"}


def translate_regex(rx: str, ere: bool, to_ere: bool = False) -> str:
    """Rewrite GNU-only escapes in one regex to BSD spelling. With to_ere, a
    basic regex is also rewritten as an extended one (for `\\|`, which basic
    BSD regexes cannot express): BRE operators lose their backslash and ERE
    metacharacters that BRE reads literally gain one."""
    out, i, n = [], 0, len(rx)
    at_start = True  # start of the regex, a group or an alternative
    while i < n:
        ch = rx[i]
        if ch == "[":
            j = _read_bracket(rx, i)
            out.append(rx[i:j])  # GNU also reads a backslash in brackets literally
            i = j
            at_start = False
            continue
        if ch != "\\" or i + 1 >= n:
            if to_ere:
                if ch in "(){}|+?" or (ch == "*" and at_start) or (ch == "^" and not at_start):
                    ch = "\\" + ch
                elif ch == "$" and not (i + 1 == n or rx.startswith("\\)", i + 1) or rx.startswith("\\|", i + 1)):
                    ch = "\\$"
            out.append(ch)
            at_start = to_ere and ch == "^" and at_start
            i += 1
            continue
        nxt = rx[i + 1]
        if to_ere and nxt in BRE_TO_ERE:
            out.append(BRE_TO_ERE[nxt])
            at_start = nxt in "(|"
            i += 2
            continue
        at_start = False
        if nxt in CLASS_FOR:
            out.append(CLASS_FOR[nxt])
        elif nxt == "b":
            prev = "".join(out)[-1:]
            after = rx[i + 2:i + 3]
            starts = after in WORD_CHARS or after in ("(", "[") or rx.startswith("\\(", i + 2) \
                or rx[i + 2:i + 4] in ("\\w", "\\<")
            ends = prev in WORD_CHARS or prev in (")", "]", "*", "+", "?", "}")
            if starts and not ends:
                out.append("[[:<:]]")
            elif ends and not starts:
                out.append("[[:>:]]")
            else:
                raise Untranslatable()  # mid-word or empty: no BSD equivalent
        elif nxt == "B":
            raise Untranslatable()
        elif not ere and nxt == "+":
            out.append("\\{1,\\}")
        elif not ere and nxt == "?":
            out.append("\\{0,1\\}")
        elif not ere and nxt == "|":
            raise Untranslatable()
        else:
            out.append(rx[i:i + 2])
        i += 2
    return "".join(out)


def _read_delimited(script: str, i: int, delim: str, regex: bool):
    """Read from script[i] to the next unescaped delim; return (text, index past delim)."""
    j = i
    while j < len(script):
        c = script[j]
        if c == "\\":
            j += 2
            continue
        if regex and c == "[" and delim != "[":
            j = _read_bracket(script, j)
            continue
        if c == delim:
            return script[i:j], j + 1
        if c == "\n":
            break
        j += 1
    raise Untranslatable()


def translate_script(script: str, ere: bool, to_ere: bool = False) -> str:
    """Translate the regexes of a sed script made of addresses and s///, p, d
    and similar one-letter commands. Anything else is left alone, and refused
    if a GNU escape appears in it."""
    out, i, n = [], 0, len(script)

    def address(i):
        if i < n and script[i] == "/":
            rx, j = _read_delimited(script, i + 1, "/", True)
            out.append("/" + translate_regex(rx, ere, to_ere) + "/")
            return j
        if i + 1 < n and script[i] == "\\":
            d = script[i + 1]
            rx, j = _read_delimited(script, i + 2, d, True)
            out.append("\\" + d + translate_regex(rx, ere, to_ere) + d)
            return j
        j = i
        while j < n and (script[j].isdigit() or script[j] in "$~"):
            j += 1
        out.append(script[i:j])
        return j

    while i < n:
        c = script[i]
        if c in " \t\n;{}!":
            out.append(c)
            i += 1
            continue
        i = address(i)
        if i < n and script[i] == ",":
            out.append(",")
            i = address(i + 1)
        while i < n and script[i] in " \t!":
            out.append(script[i])
            i += 1
        if i >= n:
            break
        c = script[i]
        if c == "s" and i + 1 < n:
            d = script[i + 1]
            rx, j = _read_delimited(script, i + 2, d, True)
            repl, j = _read_delimited(script, j, d, False)
            k = j
            while k < n and script[k] not in ";\n}":
                k += 1
            flags = script[j:k]
            if "w" in flags or "e" in flags:
                raise Untranslatable()
            out.append("s" + d + translate_regex(rx, ere, to_ere) + d + repl + d + flags)
            i = k
        elif c in "dpqnNDPhHgGx=lz{}":
            out.append(c)
            i += 1
        else:
            rest = script[i:]
            if to_ere or GNU_ESCAPES.search(rest) or (not ere and GNU_BRE_OPERATORS.search(rest)):
                raise Untranslatable()
            out.append(rest)
            break
    return "".join(out)


def quote(s: str) -> str:
    return "'" + s.replace("'", "'\\''") + "'"


def translate_command(command: str):
    """Return the command with each sed rewritten to BSD spelling, or None if
    nothing needed rewriting. Raises Untranslatable when a needed rewrite is
    not exact."""
    words = shell_words(command)
    edits = []  # (start, end, replacement)
    seg = []
    for w in words + [(len(command), len(command), ";", True)]:
        if not w[3]:
            seg.append(w)
            continue
        names = [x[2] for x in seg]
        for j, name in enumerate(names):
            if name == "sed" or name.endswith("/sed"):
                edits.extend(_translate_invocation(seg[j + 1:]))
                break
        seg = []
    if not edits:
        return None
    for start, end, rep in sorted(edits, reverse=True):
        command = command[:start] + rep + command[end:]
    return command


def _translate_invocation(args):
    vals = [a[2] for a in args]
    ere = any(v in ("-E", "-r") or (v.startswith("-") and not v.startswith("--") and len(v) > 1
                                    and set(v[1:]) <= set("Enrsu") and ("E" in v or "r" in v)) for v in vals)
    to_ere = not ere and any("\\|" in v for v in vals)
    edits, i, script_seen = [], 0, False
    if to_ere:
        if "-f" in vals or "--file" in vals or not args:
            raise Untranslatable()
        edits.append((args[0][0], args[0][0], "-E "))
    while i < len(args):
        start, end, v, _ = args[i]
        if v == "-i":
            nxt = vals[i + 1] if i + 1 < len(vals) else ""
            if nxt.startswith("-") or (nxt and ("/" in nxt or nxt[:1] in "sy")):
                edits.append((start, end, "-i ''"))
                i += 1
                continue
            i += 2
            continue
        if v in ("-e", "--expression"):
            if i + 1 < len(args):
                edits.extend(_script_edit(args[i + 1], ere, to_ere))
            script_seen = True
            i += 2
            continue
        if v in ("-f", "--file", "-l"):
            i += 2
            continue
        if v.startswith("-") and len(v) > 1:
            i += 1
            continue
        if not script_seen:
            edits.extend(_script_edit(args[i], ere, to_ere))
            script_seen = True
        i += 1
    return edits


def _script_edit(word, ere, to_ere):
    start, end, value, _ = word
    if not to_ere and not (GNU_ESCAPES.search(value) or (not ere and GNU_BRE_OPERATORS.search(value))):
        return []
    new = translate_script(value, ere, to_ere)
    return [] if new == value else [(start, end, quote(new))]


def settled(command: str) -> bool:
    """The rewrite needs no further rewriting: a second pass is a no-op."""
    try:
        return translate_command(command) is None
    except Untranslatable:
        return False


def main():
    payload = json.load(sys.stdin)
    command = (payload.get("tool_input") or {}).get("command") or ""
    if "sed" not in command or "<<" in command:
        return
    try:
        invocations = list(sed_invocations(command))
    except ValueError:
        return
    if not invocations or not is_bsd_sed():
        return
    found = []
    for args in invocations:
        for p in problems(args):
            if p not in found:
                found.append(p)
    if not found:
        return
    try:
        rewritten = translate_command(command)
    except Untranslatable:
        rewritten = None
    if rewritten and settled(rewritten):
        # No permissionDecision: the rewritten command goes through the
        # user's normal permission flow, exactly as the original would have.
        print(json.dumps({
            "hookSpecificOutput": {
                "hookEventName": "PreToolUse",
                "updatedInput": {"command": rewritten},
                "additionalContext": "This machine's sed is BSD (macOS) sed, so the command was run in its "
                                     "BSD spelling: " + rewritten,
            }
        }))
        return
    print(json.dumps({
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": "Not run: " + " ".join(found),
        }
    }))


if __name__ == "__main__":
    main()
