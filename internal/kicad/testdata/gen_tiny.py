# Generates tiny.kicad_pcb, the synthetic live-test board of `kicad place`
# (MIT, pcbpilot's own): a 50 x 40 mm 2-layer board with an 8-pad IC,
# decaps, resistors (R3 on the bottom), a locked header, a mounting hole
# and one track.
#
#   <KiCad python3> gen_tiny.py tiny.kicad_pcb
import sys

import pcbnew

mm = pcbnew.FromMM
board = pcbnew.BOARD()
nets = {}


def net(name):
    if name not in nets:
        n = pcbnew.NETINFO_ITEM(board, name)
        board.Add(n)
        nets[name] = n
    return nets[name]


def edge(x0, y0, x1, y1):
    s = pcbnew.PCB_SHAPE(board)
    s.SetShape(pcbnew.SHAPE_T_RECT)
    s.SetStart(pcbnew.VECTOR2I(mm(x0), mm(y0)))
    s.SetEnd(pcbnew.VECTOR2I(mm(x1), mm(y1)))
    s.SetLayer(pcbnew.Edge_Cuts)
    s.SetWidth(mm(0.1))
    board.Add(s)


def footprint(ref, lib, x, y, rot, pads, court, kind=pcbnew.PAD_ATTRIB_SMD, size=(0.9, 0.9)):
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(ref)
    fp.SetFPID(pcbnew.LIB_ID(lib.split(":")[0], lib.split(":")[1]))
    fp.SetPosition(pcbnew.VECTOR2I(mm(x), mm(y)))  # first: children are added in board coordinates
    fp.Reference().SetTextSize(pcbnew.VECTOR2I(mm(1), mm(1)))
    fp.Reference().SetTextThickness(mm(0.15))
    fp.Reference().SetPosition(pcbnew.VECTOR2I(mm(x), mm(y - court[1] - 1)))
    board.Add(fp)
    for num, netname, dx, dy in pads:
        p = pcbnew.PAD(fp)
        p.SetNumber(num)
        p.SetAttribute(kind)
        if kind == pcbnew.PAD_ATTRIB_SMD:
            p.SetLayerSet(p.SMDMask())
            p.SetShape(pcbnew.F_Cu, pcbnew.PAD_SHAPE_RECT) if hasattr(pcbnew, "PADSTACK") else p.SetShape(pcbnew.PAD_SHAPE_RECT)
        else:
            p.SetLayerSet(p.PTHMask() if kind == pcbnew.PAD_ATTRIB_PTH else p.UnplatedHoleMask())
            d = mm(size[0] * 0.6 if kind == pcbnew.PAD_ATTRIB_PTH else size[0])
            p.SetDrillSize(pcbnew.VECTOR2I(d, d))
        p.SetSize(pcbnew.VECTOR2I(mm(size[0]), mm(size[1])))
        p.SetPosition(pcbnew.VECTOR2I(mm(x + dx), mm(y + dy)))
        if netname:
            p.SetNet(net(netname))
        fp.Add(p)
    s = pcbnew.PCB_SHAPE(fp)
    s.SetShape(pcbnew.SHAPE_T_RECT)
    s.SetStart(pcbnew.VECTOR2I(mm(x - court[0]), mm(y - court[1])))
    s.SetEnd(pcbnew.VECTOR2I(mm(x + court[0]), mm(y + court[1])))
    s.SetLayer(pcbnew.F_CrtYd)
    s.SetWidth(mm(0.05))
    fp.Add(s)
    if rot:
        fp.SetOrientationDegrees(rot)
    return fp


def two(a, b):
    return [("1", a, -0.8, 0), ("2", b, 0.8, 0)]


edge(0, 0, 50, 40)
ic = [(str(i + 1), n, -2.5 if i < 4 else 2.5, -1.9 + 1.27 * (i % 4)) for i, n in enumerate(["VCC", "A", "B", "GND", "C", "D", "VCC", "GND"])]
footprint("U1", "pcbpilot_test:IC8", 25, 20, 0, ic, (3.6, 2.8), size=(1.4, 0.6))
footprint("C1", "pcbpilot_test:C0603", 10, 8, 0, two("VCC", "GND"), (1.5, 0.8))
footprint("C2", "pcbpilot_test:C0603", 40, 8, 90, two("VCC", "GND"), (1.5, 0.8))
footprint("R1", "pcbpilot_test:R0603", 10, 32, 0, two("A", "C"), (1.5, 0.8))
footprint("R2", "pcbpilot_test:R0603", 40, 32, 30, two("B", "D"), (1.5, 0.8))
r3 = footprint("R3", "pcbpilot_test:R0603", 25, 8, 0, two("A", "GND"), (1.5, 0.8))
r3.Flip(r3.GetPosition(), pcbnew.FLIP_DIRECTION_LEFT_RIGHT)
r3.SetOrientationDegrees(45)
j1 = footprint("J1", "pcbpilot_test:Header1x02", 3, 20, 90, [("1", "VCC", 0, -1.27), ("2", "GND", 0, 1.27)], (1.3, 2.6),
               kind=pcbnew.PAD_ATTRIB_PTH, size=(1.7, 1.7))
j1.SetLocked(True)
footprint("H1", "MountingHole:MountingHole_3.2mm_M3", 46, 36, 0, [("", "", 0, 0)], (3.0, 3.0),
          kind=pcbnew.PAD_ATTRIB_NPTH, size=(3.2, 3.2))
t = pcbnew.PCB_TRACK(board)
t.SetStart(pcbnew.VECTOR2I(mm(10.8), mm(8)))
t.SetEnd(pcbnew.VECTOR2I(mm(22.5), mm(18.1)))
t.SetWidth(mm(0.25))
t.SetLayer(pcbnew.F_Cu)
t.SetNet(net("GND"))
board.Add(t)
board.Save(sys.argv[1])
