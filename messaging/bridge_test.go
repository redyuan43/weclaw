package messaging

import (
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
)

func TestBridgeEligibleRoutingModes(t *testing.T) {
	bridge := NewBridgeClient(BridgeConfig{
		Enabled:        true,
		Endpoint:       "http://127.0.0.1:8781/im/weclaw/inbound",
		ChatAllowlist:  map[string]struct{}{"user@im.wechat": {}},
		IgnorePrefixes: []string{"[A2A-BRIDGE] "},
		Timeout:        2 * time.Second,
	})
	msg := ilink.WeixinMessage{FromUserID: "user@im.wechat"}

	route, text, allowed, suppressed := bridge.Eligible(msg, "/a2a hello")
	if route != "explicit_a2a" || text != "hello" || !allowed || suppressed {
		t.Fatalf("unexpected /a2a routing: route=%s text=%q allowed=%v suppressed=%v", route, text, allowed, suppressed)
	}

	route, text, allowed, suppressed = bridge.Eligible(msg, "/local hi")
	if route != "explicit_local" || text != "hi" || !allowed || suppressed {
		t.Fatalf("unexpected /local routing: route=%s text=%q allowed=%v suppressed=%v", route, text, allowed, suppressed)
	}

	route, text, allowed, suppressed = bridge.Eligible(msg, "plain text")
	if route != "auto" || text != "plain text" || !allowed || suppressed {
		t.Fatalf("unexpected auto routing: route=%s text=%q allowed=%v suppressed=%v", route, text, allowed, suppressed)
	}

	route, text, allowed, suppressed = bridge.Eligible(msg, "[A2A-BRIDGE] final")
	if allowed || !suppressed || text != "final" {
		t.Fatalf("expected suppressed bridge message, got route=%s text=%q allowed=%v suppressed=%v", route, text, allowed, suppressed)
	}
}

func TestInboxStoreFiltersByAfterSeq(t *testing.T) {
	store := NewInboxStore(10)
	store.Append(InboxRecord{FromUserID: "a", Text: "one"})
	store.Append(InboxRecord{FromUserID: "b", Text: "two"})
	store.Append(InboxRecord{FromUserID: "a", Text: "three"})

	items := store.List("a", 1, 10)
	if len(items) != 1 || items[0].Text != "three" {
		t.Fatalf("unexpected inbox items: %#v", items)
	}
}
