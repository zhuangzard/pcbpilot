#!/usr/bin/env python3
"""Vendor a subset of the official EasyEDA format JSON Schemas into schemas/.

Source: https://github.com/easyeda/easyeda-format-skill (MIT, Copyright (c) 2026
EasyEDA). Only the schemas `pcbpilot project inspect-eprj3` validates against are
copied. Modification (recorded in NOTICE): the long human-readable `description`
and `enumDescriptions` keys are stripped to keep the embedded copy small; every
validation keyword (type / properties / required / enum / items / minimum /
maximum / pattern / anyOf) is kept verbatim. The MIT license is copied next to
the files and SOURCE.json pins the upstream commit.

Re-run after bumping the pinned upstream commit:

    python3 internal/eprj3/vendor_schemas.py --src <easyeda-format-skill checkout>
"""
import argparse
import json
import os
import shutil
import subprocess

HERE = os.path.dirname(os.path.abspath(__file__))
OUT = os.path.join(HERE, 'schemas')

# Keep in sync with schemaFor() in validate.go.
SCHEMAS = [
    't-doc-head',
    # SCH_PAGE
    'tm-sheet', 't-sch-canvas', 'tm-sch-component', 't-wire', 't-sch-line', 't-sch-attr',
    # SCH
    'tm-schematic',
    # PCB
    'tm-pcb', 't-canvas', 'tm-pcb-component', 't-net', 't-pcb-via', 't-pcb-line',
    't-pcb-arc', 't-pcb-pour', 't-pcb-poured', 't-pad-net-wire', 't-pcb-attr', 't-pcb-pad',
    # PANEL
    'tm-panel', 't-panel-canvas',
]
DROP = {'description', 'enumDescriptions'}


def strip(o):
    if isinstance(o, dict):
        return {k: strip(v) for k, v in o.items() if k not in DROP}
    if isinstance(o, list):
        return [strip(x) for x in o]
    return o


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument('--src', required=True, help='easyeda-format-skill checkout')
    a = ap.parse_args()
    os.makedirs(OUT, exist_ok=True)
    for name in SCHEMAS:
        with open(os.path.join(a.src, 'schemas', name + '.json'), encoding='utf-8') as f:
            data = strip(json.load(f))
        with open(os.path.join(OUT, name + '.json'), 'w', encoding='utf-8') as f:
            json.dump(data, f, ensure_ascii=False, indent=1, sort_keys=True)
            f.write('\n')
    shutil.copyfile(os.path.join(a.src, 'LICENSE'), os.path.join(OUT, 'LICENSE'))
    commit = subprocess.run(['git', '-C', a.src, 'rev-parse', 'HEAD'],
                            capture_output=True, text=True).stdout.strip()
    with open(os.path.join(OUT, 'SOURCE.json'), 'w', encoding='utf-8') as f:
        json.dump({
            'repository': 'https://github.com/easyeda/easyeda-format-skill',
            'commit': commit,
            'license': 'MIT (Copyright (c) 2026 EasyEDA), see LICENSE',
            'modification': 'description/enumDescriptions stripped; validation keywords verbatim',
            'schemas': SCHEMAS,
        }, f, indent=1)
        f.write('\n')
    print(f'vendored {len(SCHEMAS)} schemas from {commit or a.src}')


if __name__ == '__main__':
    main()
