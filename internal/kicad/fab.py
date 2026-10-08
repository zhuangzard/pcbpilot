#!/usr/bin/env python3
"""pcbpilot kicad lcsc --set helper (embedded in the pcbpilot binary).

  fab.py set-pcb --file X.kicad_pcb --assign '{"R1":"C25744"}' --field "LCSC Part #" --accepted '[...]'
  fab.py set-sch --file X.kicad_sch --assign ... (follows hierarchical sheets)

set-pcb needs KiCad's python (import pcbnew); the board is re-saved by KiCad.
set-sch is a surgical text edit (KiCad 10 has no schematic Python API): only
the value string of an existing LCSC-type property is replaced, or one hidden
property is inserted after the symbol's last property. Everything else in the
file is left byte-for-byte unchanged.

Prints one JSON object: {"file", "changed": {ref: "field: old -> new"}, "notFound": [refs]}.
MIT licence, part of pcbpilot.
"""
import argparse
import json
import os
import sys


def pick_field(names, accepted, default):
    """Existing accepted field to update (priority order), else the default."""
    low = {n.lower(): n for n in names}
    for a in accepted:
        if a.lower() in low:
            return low[a.lower()], True
    return default, False


# ── board ──────────────────────────────────────────────────────────────────

def set_pcb(path, assign, field, accepted):
    import pcbnew  # only available in KiCad's python
    board = pcbnew.LoadBoard(path)
    changed, found = {}, set()
    for fp in board.GetFootprints():
        ref = fp.GetReference()
        if ref not in assign:
            continue
        found.add(ref)
        texts = fp.GetFieldsText()
        name, exists = pick_field(list(texts.keys()), accepted, field)
        old = texts.get(name, '') if exists else ''
        if old == assign[ref]:
            continue
        fp.SetField(name, assign[ref])
        if not exists:
            f = fp.GetField(name)
            if f is not None:
                f.SetVisible(False)
        changed[ref] = '%s: %s -> %s' % (name, old or '(none)', assign[ref])
    if changed:
        pcbnew.SaveBoard(path, board)
    return {'file': path, 'changed': changed,
            'notFound': sorted(r for r in assign if r not in found)}


# ── schematic (text) ───────────────────────────────────────────────────────

class Node:
    __slots__ = ('start', 'end', 'kids', 'atom', 'astart', 'aend')

    def __init__(self):
        self.kids, self.atom = [], None
        self.start = self.end = self.astart = self.aend = 0

    def head(self):
        return self.kids[0].atom if self.kids and self.kids[0].atom is not None else None

    def arg(self, i):
        k = self.kids[i + 1] if i + 1 < len(self.kids) else None
        return k.atom if k is not None else None


def parse(text):
    pos, n = 0, len(text)

    def atom_at(p):
        nd = Node()
        nd.astart = p
        if text[p] == '"':
            q, buf = p + 1, []
            while text[q] != '"':
                if text[q] == '\\':
                    q += 1
                    buf.append({'n': '\n', 't': '\t'}.get(text[q], text[q]))
                else:
                    buf.append(text[q])
                q += 1
            nd.atom, nd.aend = ''.join(buf), q + 1
        else:
            q = p
            while q < n and text[q] not in ' \t\r\n()"':
                q += 1
            nd.atom, nd.aend = text[p:q], q
        nd.start, nd.end = nd.astart, nd.aend
        return nd

    stack, root = [], None
    while pos < n:
        c = text[pos]
        if c in ' \t\r\n':
            pos += 1
        elif c == '(':
            nd = Node()
            nd.start = pos
            if stack:
                stack[-1].kids.append(nd)
            stack.append(nd)
            pos += 1
        elif c == ')':
            nd = stack.pop()
            nd.end = pos + 1
            pos += 1
            if not stack:
                root = nd
                break
        else:
            a = atom_at(pos)
            stack[-1].kids.append(a)
            pos = a.aend
    return root


def quote(s):
    return '"' + s.replace('\\', '\\\\').replace('"', '\\"') + '"'


def symbol_refs(sym):
    refs = set()
    for k in sym.kids:
        if k.head() == 'property' and k.arg(0) == 'Reference':
            refs.add(k.arg(1))
        if k.head() == 'instances':
            stack = [k]
            while stack:
                x = stack.pop()
                for y in x.kids:
                    if y.head() == 'reference':
                        refs.add(y.arg(0))
                    elif y.atom is None:
                        stack.append(y)
    return refs


def set_sch_file(path, assign, field, accepted, found, changed):
    text = open(path, encoding='utf-8').read()
    root = parse(text)
    edits, subsheets = [], []
    for sym in root.kids:
        h = sym.head()
        if h == 'sheet':
            for k in sym.kids:
                if k.head() == 'property' and k.arg(0) in ('Sheetfile', 'Sheet file'):
                    subsheets.append(k.arg(1))
            continue
        if h != 'symbol':
            continue
        hits = [r for r in symbol_refs(sym) if r in assign]
        if not hits:
            continue
        ref = hits[0]
        found.add(ref)
        props = [k for k in sym.kids if k.head() == 'property']
        name, exists = pick_field([p.arg(0) for p in props], accepted, field)
        val = assign[ref]
        if exists:
            p = next(p for p in props if p.arg(0) == name)
            vnode = p.kids[2]
            if vnode.atom == val:
                continue
            edits.append((vnode.astart, vnode.aend, quote(val)))
            changed[ref] = '%s: %s -> %s' % (name, vnode.atom or '(none)', val)
        else:
            at = next((k for k in sym.kids if k.head() == 'at'), None)
            x, y = (at.arg(0), at.arg(1)) if at else ('0', '0')
            last = props[-1] if props else sym.kids[0]
            line_start = text.rfind('\n', 0, last.start) + 1
            indent = text[line_start:last.start]
            ins = ('\n%s(property %s %s\n%s\t(at %s %s 0)\n%s\t(effects\n%s\t\t(font\n'
                   '%s\t\t\t(size 1.27 1.27)\n%s\t\t)\n%s\t\t(hide yes)\n%s\t)\n%s)') % (
                indent, quote(name), quote(val), indent, x, y, indent, indent,
                indent, indent, indent, indent, indent)
            edits.append((last.end, last.end, ins))
            changed[ref] = '%s: (none) -> %s' % (name, val)
    if edits:
        for s, e, rep in sorted(edits, reverse=True):
            text = text[:s] + rep + text[e:]
        tmp = path + '.pcbpilot-tmp'
        with open(tmp, 'w', encoding='utf-8', newline='') as f:
            f.write(text)
        os.replace(tmp, path)
    base = os.path.dirname(os.path.abspath(path))
    return [os.path.join(base, s) for s in subsheets]


def set_sch(path, assign, field, accepted):
    found, changed, seen = set(), {}, set()
    queue = [os.path.abspath(path)]
    while queue:
        p = queue.pop(0)
        if p in seen or not os.path.exists(p):
            continue
        seen.add(p)
        queue.extend(set_sch_file(p, assign, field, accepted, found, changed))
    return {'file': path, 'changed': changed,
            'notFound': sorted(r for r in assign if r not in found)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('cmd', choices=['set-pcb', 'set-sch'])
    ap.add_argument('--file', required=True)
    ap.add_argument('--assign', required=True)
    ap.add_argument('--field', default='LCSC Part #')
    ap.add_argument('--accepted', default='["LCSC Part #","LCSC","LCSC Part","JLCPCB Part #"]')
    a = ap.parse_args()
    assign = json.loads(a.assign)
    accepted = json.loads(a.accepted)
    if a.field not in accepted:
        accepted = [a.field] + accepted
    fn = set_pcb if a.cmd == 'set-pcb' else set_sch
    print(json.dumps(fn(a.file, assign, a.field, accepted)))
    return 0


if __name__ == '__main__':
    sys.exit(main())
