#!/usr/bin/env python3
"""pcbpilot kicad lcsc --set helper (embedded in the pcbpilot binary).

  fab.py set-pcb --file X.kicad_pcb --assign '{"R1":"C25744"}' --field "LCSC Part #" --accepted '[...]'
  fab.py set-sch --file X.kicad_sch --assign ... (follows hierarchical sheets)
  fab.py import-lcsc --lcsc C6186 --component rec.json --lib-dir DIR --lib-name lcsc --cli kicad-cli

set-pcb needs KiCad's python (import pcbnew); the board is re-saved by KiCad.
set-sch is a surgical text edit (KiCad 10 has no schematic Python API): only
the value string of an existing LCSC-type property is replaced, or one hidden
property is inserted after the symbol's last property. Everything else in the
file is left byte-for-byte unchanged.

import-lcsc takes the part's EasyEDA (Std) library record (symbol + footprint
documents; fetched by pcbpilot's Go side from JLC's own EasyEDA server) and
converts it with KiCad's built-in EasyEDA importers only:
pcbnew's PCB_IO_MGR.EASYEDA plugin for the footprint, `kicad-cli sym upgrade`
for the symbol. No converter code is reimplemented or vendored here.

set-*: prints {"file", "changed": {ref: "field: old -> new"}, "notFound": [refs]}.
import-lcsc: prints {"lcsc", "symbol", "footprint", "symbolLib", "footprintLib", ...}.
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
                # Keep the hidden field off the silkscreen layers.
                f.SetLayer(pcbnew.B_Fab if fp.GetLayer() == pcbnew.B_Cu else pcbnew.F_Fab)
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


# ── import-lcsc ────────────────────────────────────────────────────────────

def load_component(path, lcsc):
    """The EasyEDA library record (the `result` of JLC's EasyEDA
    /api/products/<C>/components reply), fetched by the Go side."""
    r = json.load(open(path, encoding='utf-8'))
    got = ((r.get('lcsc') or {}).get('number') or '').upper()
    if got != lcsc:
        raise SystemExit('component record is for %r, not %s' % (got, lcsc))
    return r


def sym_blocks(text):
    """Top-level (symbol ...) nodes of a .kicad_sym: [(name, start, end)]."""
    root = parse(text)
    return [(k.arg(0), k.start, k.end) for k in root.kids if k.head() == 'symbol'], root


def import_lcsc(lcsc, component, lib_dir, lib_name, cli):
    import subprocess
    import tempfile
    import pcbnew
    r = load_component(component, lcsc)
    pkg = r.get('packageDetail') or {}
    if not pkg.get('dataStr'):
        raise SystemExit('%s: no footprint in the EasyEDA library record' % lcsc)
    os.makedirs(lib_dir, exist_ok=True)
    tmp = tempfile.mkdtemp(prefix='pcbpilot-lcsc-')
    fp_json = os.path.join(tmp, lcsc + '_fp.json')
    sym_json = os.path.join(tmp, lcsc + '_sym.json')
    json.dump(pkg['dataStr'], open(fp_json, 'w'))
    json.dump(r['dataStr'], open(sym_json, 'w'))

    # Footprint: KiCad's EasyEDA Std importer → .pretty
    ee = pcbnew.PCB_IO_MGR.FindPlugin(pcbnew.PCB_IO_MGR.EASYEDA)
    names = list(ee.FootprintEnumerate(fp_json))
    if not names:
        raise SystemExit('%s: KiCad EasyEDA importer found no footprint' % lcsc)
    fp = ee.FootprintLoad(fp_json, names[0])
    pretty = os.path.join(lib_dir, lib_name + '.pretty')
    os.makedirs(pretty, exist_ok=True)
    pcbnew.PCB_IO_MGR.FindPlugin(pcbnew.PCB_IO_MGR.KICAD_SEXP).FootprintSave(pretty, fp)
    fp_name = str(fp.GetFPID().GetLibItemName())

    # Symbol: KiCad's EasyEDA Std symbol importer via kicad-cli → one-part lib
    one = os.path.join(tmp, lcsc + '.kicad_sym')
    p = subprocess.run([cli, 'sym', 'upgrade', sym_json, '-o', one],
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    if p.returncode != 0 or not os.path.exists(one):
        raise SystemExit('%s: kicad-cli sym upgrade failed: %s' % (lcsc, p.stdout.decode(errors='replace')))
    text = open(one, encoding='utf-8').read()
    blocks, _ = sym_blocks(text)
    if len(blocks) != 1:
        raise SystemExit('%s: expected one symbol, got %d' % (lcsc, len(blocks)))
    old = blocks[0][0]
    new = '%s_%s' % (old, lcsc) if not old.endswith('_' + lcsc) else old
    block = text[blocks[0][1]:blocks[0][2]]
    import re
    # Top symbol and its unit sub-symbols ("<name>_<unit>_<style>").
    block = re.sub(r'\(symbol "%s((?:_\d+_\d+)?)"' % re.escape(old),
                   lambda m: '(symbol "%s%s"' % (new, m.group(1)), block)
    block = block.replace('(property "Footprint" ""', '(property "Footprint" %s' % quote(lib_name + ':' + fp_name), 1)
    # The JLC/LCSC number as the field `pcbpilot kicad fab` (and JLC's tools) read.
    sub = parse(block)
    fpn = next(k for k in sub.kids if k.head() == 'property' and k.arg(0) == 'Footprint')
    line_start = block.rfind('\n', 0, fpn.start) + 1
    ind = block[line_start:fpn.start]
    ins = ('\n%s(property "LCSC Part #" %s\n%s\t(at 0 0 0)\n%s\t(effects\n%s\t\t(font\n%s\t\t\t(size 1.27 1.27)\n'
           '%s\t\t)\n%s\t\t(hide yes)\n%s\t)\n%s)') % ((ind, quote(lcsc)) + (ind,) * 8)
    block = block[:fpn.end] + ins + block[fpn.end:]

    lib = os.path.join(lib_dir, lib_name + '.kicad_sym')
    if os.path.exists(lib):
        lt = open(lib, encoding='utf-8').read()
        lblocks, _ = sym_blocks(lt)
        hit = [b for b in lblocks if b[0] == new]
        if hit:
            _, s0, e0 = hit[0]
            lt = lt[:s0] + block + lt[e0:]
        else:
            end = lt.rstrip().rfind(')')
            lt = lt[:end].rstrip('\n') + '\n\t' + block + '\n' + lt[end:]
    else:
        lt = text[:blocks[0][1]] + block + text[blocks[0][2]:]
    with open(lib + '.pcbpilot-tmp', 'w', encoding='utf-8', newline='') as f:
        f.write(lt)
    os.replace(lib + '.pcbpilot-tmp', lib)
    c_para = (r.get('dataStr') or {}).get('head', {}).get('c_para', {})
    return {'lcsc': lcsc, 'title': r.get('title'),
            'symbol': lib_name + ':' + new, 'footprint': lib_name + ':' + fp_name,
            'symbolLib': lib, 'footprintLib': pretty,
            'jlcPartClass': c_para.get('JLCPCB Part Class'),
            'note': 'converted by KiCad 10 EasyEDA importers; no 3D model; review pins/pads against the datasheet'}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('cmd', choices=['set-pcb', 'set-sch', 'import-lcsc'])
    ap.add_argument('--file')
    ap.add_argument('--assign')
    ap.add_argument('--lcsc')
    ap.add_argument('--component')
    ap.add_argument('--lib-dir')
    ap.add_argument('--lib-name', default='lcsc')
    ap.add_argument('--cli', default='kicad-cli')
    ap.add_argument('--field', default='LCSC Part #')
    ap.add_argument('--accepted', default='["LCSC Part #","LCSC","LCSC Part","JLCPCB Part #"]')
    a = ap.parse_args()
    if a.cmd == 'import-lcsc':
        print(json.dumps(import_lcsc(a.lcsc.upper(), a.component, a.lib_dir, a.lib_name, a.cli)))
        return 0
    assign = json.loads(a.assign)
    accepted = json.loads(a.accepted)
    if a.field not in accepted:
        accepted = [a.field] + accepted
    fn = set_pcb if a.cmd == 'set-pcb' else set_sch
    print(json.dumps(fn(a.file, assign, a.field, accepted)))
    return 0


if __name__ == '__main__':
    sys.exit(main())
