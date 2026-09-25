package daemon

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/zhuangzard/pcbpilot/internal/protocol"
)

// Only an unidentified host is refused before dispatch; V3 and V4 both
// support net labels (V3 through the connector's wire-name fallback).
func TestNativeLabelRefusedBeforeConnectorDispatch(t *testing.T) {
	for _, version := range []string{"", "2.2.40"} {
		t.Run(version, func(t *testing.T) {
			base, cleanup := startDaemon(t)
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c, _, err := websocket.Dial(ctx, "ws://"+base+"/eda", nil)
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseNow()
			if err := wsjson.Write(ctx, c, protocol.Register{Type: protocol.TypeRegister, WindowID: "v3", EasyEDAVersion: version}); err != nil {
				t.Fatal(err)
			}
			waitForWindow(t, base, "v3")
			for _, action := range []string{"schematic.netflag.create", "schematic.power.connect_pin"} {
				res := postAction(t, base, fmt.Sprintf(`{"action":%q,"windowId":"v3","payload":{"kind":"net_label","pinX":0,"pinY":0,"net":"SIG"}}`, action))
				if res.OK || res.Error == nil || res.Error.Code != "HOST_API_UNSUPPORTED" {
					t.Fatalf("%s: %+v", action, res)
				}
			}
			// The connector never answers: an accidental dispatch would time out,
			// not return the immediate capability error above.
		})
	}
}
