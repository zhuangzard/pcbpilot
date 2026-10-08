# gen_tiny.py — builds testdata/tiny.kicad_pcb (pcbpilot's own MIT fixture)
# with KiCad's Python: a 2-layer 40 x 30 mm board, five two-pad SMD parts,
# one through-hole header and an NPTH mounting hole, nets VCC / GND / SIG_A /
# SIG_B, no tracks. Re-run after a KiCad format change:
#
#   <KiCad python> internal/kicad/testdata/gen_tiny.py internal/kicad/testdata/tiny.kicad_pcb
import sys

import pcbnew

MM = pcbnew.FromMM


def vec(x, y):
    return pcbnew.VECTOR2I(MM(x), MM(y))


def add_net(board, name):
    net = pcbnew.NETINFO_ITEM(board, name)
    board.Add(net)
    return net


def two_pad(board, ref, value, x, y, rot, nets, lcsc=""):
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue(value)
    fp.SetFPIDAsString("pcbpilot:R_1206_tiny")
    for i, (dx, net) in enumerate(((-1.6, nets[0]), (1.6, nets[1]))):
        p = pcbnew.PAD(fp)
        p.SetNumber(str(i + 1))
        p.SetAttribute(pcbnew.PAD_ATTRIB_SMD)
        p.SetShape(pcbnew.PAD_SHAPE_RECT)
        p.SetSize(vec(1.2, 1.8))
        p.SetLayerSet(p.SMDMask())
        p.SetPosition(vec(dx, 0))
        p.SetNet(net)
        fp.Add(p)
    if lcsc:
        fp.SetField("LCSC", lcsc)
    board.Add(fp)
    fp.SetPosition(vec(x, y))
    fp.SetOrientationDegrees(rot)
    return fp


def header(board, ref, x, y, nets):
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue("HDR_1x3")
    fp.SetFPIDAsString("pcbpilot:PinHeader_1x3_tiny")
    for i, net in enumerate(nets):
        p = pcbnew.PAD(fp)
        p.SetNumber(str(i + 1))
        p.SetAttribute(pcbnew.PAD_ATTRIB_PTH)
        p.SetShape(pcbnew.PAD_SHAPE_RECT if i == 0 else pcbnew.PAD_SHAPE_CIRCLE)
        p.SetSize(vec(1.7, 1.7))
        p.SetDrillSize(vec(1.0, 1.0))
        p.SetLayerSet(p.PTHMask())
        p.SetPosition(vec(0, i * 2.54))
        p.SetNet(net)
        fp.Add(p)
    board.Add(fp)
    fp.SetPosition(vec(x, y))
    return fp


def mounting_hole(board, ref, x, y):
    fp = pcbnew.FOOTPRINT(board)
    fp.SetReference(ref)
    fp.SetValue("MountingHole_2.2mm")
    fp.SetFPIDAsString("pcbpilot:MountingHole_tiny")
    p = pcbnew.PAD(fp)
    p.SetNumber("")
    p.SetAttribute(pcbnew.PAD_ATTRIB_NPTH)
    p.SetShape(pcbnew.PAD_SHAPE_CIRCLE)
    p.SetSize(vec(2.2, 2.2))
    p.SetDrillSize(vec(2.2, 2.2))
    p.SetLayerSet(p.UnplatedHoleMask())
    fp.Add(p)
    board.Add(fp)
    fp.SetPosition(vec(x, y))


def main(out):
    board = pcbnew.BOARD()
    board.SetCopperLayerCount(2)
    vcc, gnd, a, b = (add_net(board, n) for n in ("VCC", "GND", "SIG_A", "SIG_B"))
    corners = [(0, 0), (40, 0), (40, 30), (0, 30)]
    for i in range(4):
        s = pcbnew.PCB_SHAPE(board)
        s.SetShape(pcbnew.SHAPE_T_SEGMENT)
        s.SetLayer(pcbnew.Edge_Cuts)
        s.SetWidth(MM(0.1))
        s.SetStart(vec(*corners[i]))
        s.SetEnd(vec(*corners[(i + 1) % 4]))
        board.Add(s)
    header(board, "J1", 5, 10, [vcc, gnd, a])
    two_pad(board, "R1", "10k", 15, 8, 0, [vcc, a], lcsc="C25804")
    two_pad(board, "R2", "10k", 15, 16, 90, [a, b])
    two_pad(board, "C1", "100nF", 25, 8, 0, [vcc, gnd], lcsc="C14663")
    two_pad(board, "R3", "1k", 30, 18, 0, [b, gnd])
    two_pad(board, "C2", "1uF", 30, 24, 180, [vcc, gnd])
    mounting_hole(board, "H1", 36, 4)
    ds = board.GetDesignSettings()
    ds.m_TrackMinWidth = MM(0.15)
    ds.m_MinClearance = MM(0.15)
    pcbnew.SaveBoard(out, board)


if __name__ == "__main__":
    main(sys.argv[1])
