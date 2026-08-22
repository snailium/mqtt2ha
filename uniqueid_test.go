package main

import (
	"strings"
	"testing"
)

// P2 #9: entityUniqueID / deviceKey must disambiguate topics that sanitize()
// collapses to the same string (e.g. "home/ups/ups" vs "home_ups_ups").
func TestUniqueIDCollisionResolution(t *testing.T) {
	// Two distinct topics that sanitize() maps to the same form.
	t1 := &Device{Topic: "home/ups/ups"}
	t2 := &Device{Topic: "home_ups_ups"}
	e := Entity{Field: "voltage"}

	uid1 := entityUniqueID(t1, e)
	uid2 := entityUniqueID(t2, e)
	if uid1 == uid2 {
		t.Fatalf("P2#9 FAIL: entity unique_ids collide for %s / %s: %s", t1.Topic, t2.Topic, uid1)
	}

	k1 := deviceKey(t1.Topic)
	k2 := deviceKey(t2.Topic)
	if k1 == k2 {
		t.Fatalf("P2#9 FAIL: device identifiers collide: %s", k1)
	}

	// Both remain stable (deterministic).
	if entityUniqueID(t1, e) != uid1 {
		t.Fatal("entityUniqueID not deterministic")
	}
	// unique_id is still a valid HA object_id segment (no '/').
	if strings.Contains(uid1, "/") || strings.Contains(k1, "/") {
		t.Fatalf("unique_id/device key contains '/' -> invalid HQ topic segment: %s / %s", uid1, k1)
	}
}

// P2 #9: topicHash is short, hex, deterministic.
func TestTopicHash(t *testing.T) {
	h1 := topicHash("home/ups/ups")
	h2 := topicHash("home/ups/ups")
	if h1 != h2 || len(h1) != 6 {
		t.Fatalf("topicHash not deterministic/short: %q", h1)
	}
	if topicHash("home/ups/ups") == topicHash("home_ups_ups") {
		t.Fatal("topicHash collision for sanitize-identical inputs")
	}
}

// Publications produce discovery topics under the custom prefix (P0 #3) with a
// valid, collision-free unique_id.
func TestBuildDiscoveryCustomPrefixStable(t *testing.T) {
	dev := &Device{Topic: "home/ups/ups", Name: "ups"}
	ent := Entity{Field: "battery_percent", Unit: "%"}
	topic, payload, err := BuildDiscovery("myha", dev, ent, dev.Topic)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(topic, "myha/sensor/") {
		t.Fatalf("custom prefix not honored: %s", topic)
	}
	if strings.Contains(string(payload), `"unique_id":"mqtt2ha_v2_`) {
		t.Fatalf("stale v2 unique_id emitted: %s", payload)
	}
	// disabled entity clear path builds a removal topic under the custom prefix
	// (encapsulated in publishDevice; here we verify the prefix plumbing via
	// BuildDiscovery's topic shape only).
	_ = topic
}
