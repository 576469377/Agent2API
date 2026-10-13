package obs

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLegacyImageCountIsNotInventedAsMeasuredZero(t *testing.T) {
	var old Record
	if err := json.Unmarshal([]byte(`{"model":"hy3","status":400}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.ImageCount != nil || old.MaxImages != nil {
		t.Fatal("old records did not measure image counts")
	}
	serialized, _ := json.Marshal(old)
	if strings.Contains(string(serialized), "image_count") || strings.Contains(string(serialized), "max_images") {
		t.Fatal("legacy unknown counts must stay absent after persistence")
	}
	zero := 0
	old.ImageCount = &zero
	serialized, _ = json.Marshal(old)
	if !strings.Contains(string(serialized), `"image_count":0`) {
		t.Fatal("newly measured zero must remain explicit")
	}
}
