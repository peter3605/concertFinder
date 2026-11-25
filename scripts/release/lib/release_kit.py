#!/usr/bin/env python3
"""The parsing half of the iOS release kit. The shell scripts own the order of a
release; everything that reads a file and decides whether it is right is here,
where it can be tested without a Mac.

    release_kit.py version        FILE                 print "MARKETING_VERSION BUILD"
    release_kit.py set-build      FILE N               rewrite every CURRENT_PROJECT_VERSION to N
    release_kit.py entitlements   PLIST [--source] SPEC...
    release_kit.py store-copy     DOC --fields A,B,...
    release_kit.py log-privacy    --dirs D... [--allow FILE:EXPR ...]
    release_kit.py export-options PLIST
    release_kit.py info-plist     PLIST
    release_kit.py privacy-manifest PLIST
    release_kit.py strict-xml     FILE...              parse each as XML with no leniency

Every subcommand prints what it checked and exits 1 on a failure, 0 otherwise,
2 on bad usage.

Stdlib only, and Python 3.9 at the newest: that is the python3 the Xcode
command-line tools ship, and it is the one a release machine is sure to have.
"""
import argparse
import os
import plistlib
import re
import sys
import unicodedata
from xml.parsers.expat import ExpatError

sys.dont_write_bytecode = True

# --------------------------------------------------------------------------
# Build and marketing version
#
# Two file shapes carry them: an XcodeGen project.yml (`KEY: value`, value
# optionally quoted) and an .xcconfig (`KEY = value`). A project with the
# number in more than one place is fine as long as every copy agrees; one that
# disagrees with itself is refused, because bumping "the" number there is
# a guess about which copy the build reads.

def _setting_re(key):
    return re.compile(
        r'^(?P<lead>\s*' + key + r'\s*[:=]\s*)(?P<q>"?)(?P<val>[^"\s#/]+)(?P=q)(?P<tail>.*)$'
    )


def read_setting(text, key):
    vals = []
    for line in text.splitlines():
        m = _setting_re(key).match(line)
        if m:
            vals.append(m.group("val"))
    return vals


def read_versions(path):
    with open(path, encoding="utf-8") as f:
        text = f.read()
    builds = read_setting(text, "CURRENT_PROJECT_VERSION")
    versions = read_setting(text, "MARKETING_VERSION")
    problems = []
    if not builds:
        problems.append("no CURRENT_PROJECT_VERSION")
    elif len(set(builds)) > 1:
        problems.append("CURRENT_PROJECT_VERSION disagrees with itself: " + ", ".join(builds))
    elif not builds[0].isdigit():
        problems.append("CURRENT_PROJECT_VERSION is not a whole number: " + builds[0])
    if not versions:
        problems.append("no MARKETING_VERSION")
    elif len(set(versions)) > 1:
        problems.append("MARKETING_VERSION disagrees with itself: " + ", ".join(versions))
    if problems:
        raise ValueError(f"{path}: " + "; ".join(problems))
    return versions[0], int(builds[0])


def set_build(path, number):
    with open(path, encoding="utf-8") as f:
        lines = f.read().splitlines(keepends=True)
    rx = _setting_re("CURRENT_PROJECT_VERSION")
    changed = 0
    out = []
    for line in lines:
        body = line.rstrip("\r\n")
        end = line[len(body):]
        m = rx.match(body)
        if m:
            body = f'{m.group("lead")}{m.group("q")}{number}{m.group("q")}{m.group("tail")}'
            changed += 1
        out.append(body + end)
    if not changed:
        raise ValueError(f"{path}: no CURRENT_PROJECT_VERSION to set")
    with open(path, "w", encoding="utf-8") as f:
        f.writelines(out)
    return changed


# --------------------------------------------------------------------------
# Entitlements
#
# SPEC is "BUNDLE|KEY" or "BUNDLE|KEY=VALUE". BUNDLE is the bundle's path
# inside the .app ("." for the app itself); the caller has already picked the
# PLIST for it, so here it is only a label. VALUE must equal a string or
# boolean value, or be one member of an array value.
#
# --source checks the entitlements file in the repo rather than a signature,
# and compares only array members. A string there is what signing rewrites -
# aps-environment is "development" in source and "production" in an App Store
# export - so comparing it would fail every correctly configured project.

def parse_spec(spec):
    if "|" not in spec:
        raise ValueError(f"entitlement spec {spec!r} is not BUNDLE|KEY[=VALUE]")
    bundle, rest = spec.split("|", 1)
    key, sep, value = rest.partition("=")
    if not key:
        raise ValueError(f"entitlement spec {spec!r} names no key")
    return bundle, key, (value if sep else None)


def check_entitlements(ents, specs, source=False):
    """[(ok, message)] for each spec against one parsed entitlements dict."""
    results = []
    for spec in specs:
        bundle, key, want = parse_spec(spec)
        label = f"{bundle} {key}"
        if key not in ents:
            results.append((False, f"{label}: MISSING"))
            continue
        have = ents[key]
        if want is None:
            results.append((True, f"{label}: present"))
        elif isinstance(have, list):
            ok = want in [str(x) for x in have]
            results.append((ok, f"{label}: {want} " + ("present" if ok else f"MISSING (has {have})")))
        elif source:
            results.append((True, f"{label}: present (value is set at signing)"))
        else:
            if isinstance(have, bool):
                ok = str(have).lower() == want.lower()
            else:
                ok = str(have) == want
            results.append((ok, f"{label}: " + (f"{have}" if ok else f"{have!r}, expected {want!r}")))
    return results


COMMENT_RE = re.compile(rb"<!--.*?-->", re.DOTALL)


def load_plist(path, strict=False):
    with open(path, "rb") as f:
        data = f.read()
    # codesign prints a blob header before the XML on some macOS versions.
    start = data.find(b"<?xml")
    if start > 0:
        data = data[start:]
    if not data.strip():
        return {}
    try:
        return plistlib.loads(data)
    except ExpatError as e:
        if strict:
            raise ValueError(f"{os.path.basename(path)} is not well-formed XML ({e}). "
                             "App Store processing parses a bundled privacy manifest strictly, "
                             "and a double hyphen inside a comment is the usual cause: it was "
                             "rejected as ITMS-91056 (CF-43)") from e
        # Apple's parser accepts "--" inside an XML comment and expat does not,
        # and a hand-commented Info.plist uses it as a dash. Comments carry
        # nothing a check reads, so drop them and parse what is left. That is
        # safe only for a file Xcode re-serialises (Info.plist, entitlements):
        # a privacy manifest is copied into the bundle byte for byte, so it is
        # loaded with strict=True instead. This fallback is exactly what hid
        # the defect that got build 1.0.0 (2) rejected.
        return plistlib.loads(COMMENT_RE.sub(b"", data))


def strict_xml(paths):
    """[(path, error)] for each file expat will not parse as written."""
    bad = []
    for path in paths:
        try:
            load_plist(path, strict=True)
        except (ValueError, plistlib.InvalidFileException) as e:
            bad.append((path, str(e)))
    return bad


# --------------------------------------------------------------------------
# Store copy
#
# Fields are marked in the document as
#
#   <!-- field: subtitle limit: 30 -->
#   ```
#   the exact text
#   ```
#
# Apple counts characters, not bytes, so the count is of NFC code points.

FIELD_RE = re.compile(r"<!--\s*field:\s*(\S+)\s+limit:\s*(\d+)\s*-->\s*\n```[^\n]*\n(.*?)\n?```", re.DOTALL)
PLAIN_TEXT_FIELDS = ("description", "promotional_text", "whats_new")
# Case-sensitive on purpose: "todo" is an ordinary word in a to-do app's
# description; "TODO" in capitals is a note to self.
PLACEHOLDER_RE = re.compile(r"REPLACE_ME|\bTODO\b|\bTBD\b|(?i:lorem ipsum)")


def check_store_copy(text, required):
    """(ok, [lines])."""
    lines = []
    ok = True
    matches = FIELD_RE.findall(text)
    if not matches:
        return False, ["no <!-- field: NAME limit: N --> markers found"]
    seen = set()
    for field, limit, body in matches:
        if field in seen:
            lines.append(f"  {field}: marked twice")
            ok = False
        seen.add(field)
        limit = int(limit)
        count = len(unicodedata.normalize("NFC", body))
        over = count > limit
        unfilled = not body.strip() or PLACEHOLDER_RE.search(body)
        if unfilled and field not in required:
            # A field this release does not need - What's New on a 1.0 - may
            # sit unwritten in the document until the release that does.
            lines.append(f"  {field:<18}   not required for this release")
            continue
        ok &= not over
        lines.append(f"  {field:<18} {count:>5} / {limit:<5} {'OVER' if over else 'ok'}")
        if not body.strip():
            lines.append("       ^ empty")
            ok = False
        elif PLACEHOLDER_RE.search(body):
            lines.append("       ^ still holds placeholder text")
            ok = False
        if field == "keywords":
            if " ," in body or ", " in body:
                lines.append("       ^ a space beside a comma is a wasted character")
                ok = False
            terms = body.split(",")
            if any(not t.strip() for t in terms):
                lines.append("       ^ empty keyword (a doubled or trailing comma)")
                ok = False
            dupes = sorted({t for t in terms if terms.count(t) > 1})
            if dupes:
                lines.append("       ^ duplicate keyword(s): " + ", ".join(dupes))
                ok = False
        if field in PLAIN_TEXT_FIELDS:
            for marker in ("**", "##", "]("):
                if marker in body:
                    lines.append(f"       ^ contains {marker!r} - this field does not render Markdown")
                    ok = False
    missing = [f for f in required if f not in seen]
    if missing:
        lines.append("  no marker for: " + ", ".join(missing))
        ok = False
    return ok, lines


# --------------------------------------------------------------------------
# Log privacy
#
# `Logger` interpolation is redacted by default; `privacy: .public` undoes it.
# Whether a line is redacted has no runtime signal, so it is checked in source.
# An allowlist entry is "FILE:SUBSTRING", FILE relative to the repo root.

PUBLIC_RE = re.compile(r"privacy:\s*\.public\b")


def scan_log_privacy(root, dirs, allowed):
    """[(relpath, lineno, text)] for every un-allowed `privacy: .public`."""
    allow = []
    for entry in allowed:
        f, sep, expr = entry.partition(":")
        if not sep or not expr:
            raise ValueError(f"allowlist entry {entry!r} is not FILE:SUBSTRING")
        allow.append((os.path.normpath(f), expr))
    hits = []
    for d in dirs:
        top = os.path.join(root, d)
        if not os.path.isdir(top):
            raise ValueError(f"log source directory not found: {d}")
        for base, subdirs, files in os.walk(top):
            subdirs[:] = sorted(s for s in subdirs if not s.startswith(".") and s != "build")
            for name in sorted(files):
                if not name.endswith(".swift"):
                    continue
                path = os.path.join(base, name)
                rel = os.path.normpath(os.path.relpath(path, root))
                with open(path, encoding="utf-8", errors="replace") as fh:
                    for n, line in enumerate(fh, 1):
                        if not PUBLIC_RE.search(line):
                            continue
                        if line.lstrip().startswith("//"):
                            continue
                        if any(rel == f and expr in line for f, expr in allow):
                            continue
                        hits.append((rel, n, line.rstrip()))
    return hits


# --------------------------------------------------------------------------
# ExportOptions.plist, Info.plist, PrivacyInfo.xcprivacy

def check_export_options(opts):
    problems = []
    team = str(opts.get("teamID", ""))
    if not re.fullmatch(r"[A-Z0-9]{10}", team):
        problems.append(f"teamID is {team!r}, not a 10-character team ID")
    if opts.get("method") not in ("app-store-connect", "app-store"):
        problems.append(f"method is {opts.get('method')!r}, not app-store-connect")
    if opts.get("manageAppVersionAndBuildNumber", True) is not False:
        problems.append("manageAppVersionAndBuildNumber is not false: the export would rewrite "
                        "the build number and ship one nothing in the repo records")
    return problems


def check_info_plist(info):
    problems = []
    if "ITSAppUsesNonExemptEncryption" not in info:
        problems.append("ITSAppUsesNonExemptEncryption is not set: every uploaded build will "
                        "sit at Missing Compliance until someone answers by hand")
    return problems


def check_privacy_manifest(manifest):
    problems = []
    if not isinstance(manifest, dict):
        return ["not a dictionary"]
    for key in ("NSPrivacyTracking", "NSPrivacyAccessedAPITypes"):
        if key not in manifest:
            problems.append(f"{key} is not declared")
    for i, entry in enumerate(manifest.get("NSPrivacyAccessedAPITypes", []) or []):
        if not entry.get("NSPrivacyAccessedAPIType"):
            problems.append(f"NSPrivacyAccessedAPITypes[{i}] names no API type")
        if not entry.get("NSPrivacyAccessedAPITypeReasons"):
            problems.append(f"NSPrivacyAccessedAPITypes[{i}] gives no reason")
    return problems


# --------------------------------------------------------------------------

def _report(problems, what):
    for p in problems:
        print(f"!! {what}: {p}")
    if not problems:
        print(f"   {what}: ok")
    return 1 if problems else 0


def main(argv=None):
    ap = argparse.ArgumentParser(prog="release_kit.py")
    sub = ap.add_subparsers(dest="cmd", required=True)
    p = sub.add_parser("version"); p.add_argument("file")
    p = sub.add_parser("set-build"); p.add_argument("file"); p.add_argument("number", type=int)
    p = sub.add_parser("entitlements"); p.add_argument("plist"); p.add_argument("--source", action="store_true")
    p.add_argument("--label", default=None); p.add_argument("specs", nargs="+")
    p = sub.add_parser("store-copy"); p.add_argument("doc"); p.add_argument("--fields", default="")
    p = sub.add_parser("log-privacy"); p.add_argument("--root", default=".")
    p.add_argument("--dirs", nargs="+", required=True); p.add_argument("--allow", nargs="*", default=[])
    for name in ("export-options", "info-plist", "privacy-manifest"):
        p = sub.add_parser(name); p.add_argument("plist")
    p = sub.add_parser("strict-xml"); p.add_argument("files", nargs="+")
    a = ap.parse_args(argv)

    try:
        if a.cmd == "version":
            version, build = read_versions(a.file)
            print(version, build)
            return 0
        if a.cmd == "set-build":
            set_build(a.file, a.number)
            return 0
        if a.cmd == "entitlements":
            ents = load_plist(a.plist)
            results = check_entitlements(ents, a.specs, source=a.source)
            for ok, msg in results:
                print(("   " if ok else "!! ") + (f"{a.label}: " if a.label else "") + msg)
            return 0 if all(ok for ok, _ in results) else 1
        if a.cmd == "store-copy":
            if not os.path.isfile(a.doc):
                print(f"!! store copy: {a.doc} not found")
                return 1
            fields = [f for f in a.fields.split(",") if f]
            with open(a.doc, encoding="utf-8") as f:
                ok, lines = check_store_copy(f.read(), fields)
            print("\n".join(lines))
            print(("   store copy: every field is within its App Store Connect limit" if ok
                   else "!! store copy: not ready to paste"))
            return 0 if ok else 1
        if a.cmd == "log-privacy":
            hits = scan_log_privacy(a.root, a.dirs, a.allow)
            for rel, n, line in hits:
                print(f"!! un-redacted log interpolation: {rel}:{n}: {line.strip()}")
            if hits:
                print("   Drop the `privacy: .public` so the default `.private` applies, or add the")
                print("   line to LOG_PRIVACY_ALLOWED in release.conf with a reason it carries no user data.")
                return 1
            print("   log privacy: no log line puts user content in the clear")
            return 0
        if a.cmd == "export-options":
            return _report(check_export_options(load_plist(a.plist)), os.path.basename(a.plist))
        if a.cmd == "info-plist":
            return _report(check_info_plist(load_plist(a.plist)), os.path.basename(a.plist))
        if a.cmd == "privacy-manifest":
            return _report(check_privacy_manifest(load_plist(a.plist, strict=True)),
                           os.path.basename(a.plist))
        if a.cmd == "strict-xml":
            bad = strict_xml(a.files)
            for path, err in bad:
                print(f"!! {err}")
            if not bad:
                print(f"   strict XML: {len(a.files)} file(s) parse as written")
            return 1 if bad else 0
    except (OSError, ValueError, plistlib.InvalidFileException, ExpatError) as e:
        print(f"!! {e}")
        return 1
    return 2


if __name__ == "__main__":
    sys.exit(main())
