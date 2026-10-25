package mtxconf

import (
	"bytes"
	"context"
	"testing"
	"time"
)

// A config MediaMTX takes longer to load (long holding clips) gets the verifier's allowance on top of its timeout.
func TestVerifierAllowance(t *testing.T) {
	ctx := context.Background()
	before := []byte("readTimeout: 10s\n")
	after := []byte("readTimeout: 20s\n")
	global := func(api *fakeAPI, body string) {
		api.mu.Lock()
		api.objects["/v3/config/global/get"] = body
		api.mu.Unlock()
	}
	slowReload := func(api *fakeAPI) {
		global(api, `{"readTimeout":"10s"}`)
		go func() {
			time.Sleep(400 * time.Millisecond)
			global(api, `{"readTimeout":"20s"}`)
		}()
	}
	api := &fakeAPI{objects: map[string]string{}}
	v := &Verifier{API: api, Timeout: 150 * time.Millisecond, Interval: 10 * time.Millisecond}
	slowReload(api)
	if got, err := v.Verify(ctx, before, after); err != nil || got.State != AppliedMismatch {
		t.Fatalf("without an allowance: %+v %v", got, err)
	}
	time.Sleep(400 * time.Millisecond)

	var asked []byte
	v.Allowance = func(config []byte) time.Duration {
		asked = config
		return time.Second
	}
	slowReload(api)
	if got, err := v.Verify(ctx, before, after); err != nil || got.State != AppliedVerified {
		t.Fatalf("with an allowance: %+v %v", got, err)
	}
	if !bytes.Equal(asked, after) {
		t.Errorf("the allowance was asked about %q, not the new config", asked)
	}
}
