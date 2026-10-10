#!/usr/bin/env python3
"""PreToolUse hook on Bash: deny a sed command that BSD sed (macOS) will
silently misread, and say why.

GNU regex escapes (\\b \\< \\> \\w \\W \\s \\S, and in basic mode \\+ \\? \\|)
are read by BSD sed as literal characters, so a substitution using them
matches nothing and exits 0 without changing any file. Agents then run the
tests on unchanged code, see them pass, and report the edit done: on the
2026-10-10 benchmark that silent no-op cost the only lost task and several
wasted turns, in both the native and the prism arm. BSD `-i` also takes the
next argument as a backup suffix, so `-i -e ...` edits the file and leaves a
stray `<file>-e` copy, and GNU-style `-i 's/...'` fails.

The hook explains the semantics; it never rewrites the command or proposes
one to run. Never blocks when in doubt:
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
    lexer = shlex.shlex(command.replace("\n", " ; "), posix=True, punctuation_chars=";&|()")
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
    print(json.dumps({
        "hookSpecificOutput": {
            "hookEventName": "PreToolUse",
            "permissionDecision": "deny",
            "permissionDecisionReason": "Not run: " + " ".join(found),
        }
    }))


if __name__ == "__main__":
    main()
