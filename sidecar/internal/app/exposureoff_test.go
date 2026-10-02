package app

import (
	"net/http"
	"testing"
)

// Exposure control is opt-in (the public install's default is off): its API answers 404, the status says so, and
// stream pages take no leases.
func TestExposureControlOff(t *testing.T) {
	h := newHarness(t, map[string]string{"MTXUI_EXPOSURE_CONTROL": "off"}, fast)
	h.completeSetup()
	if rec := h.do("GET", "/api/v1/exposure", nil); rec.Code != http.StatusNotFound || decode(rec)["error"] != "exposure_off" {
		t.Fatalf("exposure: %d %s", rec.Code, rec.Body)
	}
	if m := decode(h.do("GET", "/api/v1/status", nil)); m["exposureControl"] != false {
		t.Fatalf("status %v", m)
	}
	rec := h.do("POST", "/api/v1/streams", map[string]any{"name": "live/x"})
	var st streamJSON
	h.json(rec, &st)
	if m := decode(h.do("POST", "/api/v1/streams/"+itoa(st.ID)+"/lease", nil)); m["off"] != true || m["leased"] != false {
		t.Fatalf("lease %v", m)
	}
	on := newHarness(t, nil, fast)
	on.completeSetup()
	if m := decode(on.do("GET", "/api/v1/status", nil)); m["exposureControl"] != true {
		t.Fatalf("status with it on: %v", m)
	}
}
