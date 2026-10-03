package mtxconf

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestSyncTrustedProxies(t *testing.T) {
	ctx := context.Background()
	w, st := newWriter(t, fakeChecker{})
	if _, err := w.Reconcile(ctx, seed(t, params)); err != nil {
		t.Fatal(err)
	}

	// The seed's subnet: nothing to do.
	if changed, err := w.SyncTrustedProxies(ctx, params.StackSubnet); err != nil || changed {
		t.Fatalf("same subnet: changed %v, %v", changed, err)
	}
	// Not detected: nothing to do either.
	if changed, err := w.SyncTrustedProxies(ctx, netip.Prefix{}); err != nil || changed {
		t.Fatalf("no subnet: changed %v, %v", changed, err)
	}

	// A new network: all three follow, and nothing else moves.
	before, _ := os.ReadFile(w.Path)
	if changed, err := w.SyncTrustedProxies(ctx, netip.MustParsePrefix("172.18.0.7/16")); err != nil || !changed {
		t.Fatalf("new subnet: changed %v, %v", changed, err)
	}
	after, _ := os.ReadFile(w.Path)
	var conf map[string]any
	if err := yaml.Unmarshal(after, &conf); err != nil {
		t.Fatal(err)
	}
	for _, k := range trustedProxyKeys {
		if got := fmt.Sprint(conf[k]); got != "[172.18.0.0/16]" {
			t.Errorf("%s: %s", k, got)
		}
	}
	if old := strings.ReplaceAll(string(before), "172.29.42.0/24", ""); len(after) > len(old)+3*40 {
		t.Errorf("more than the three keys changed:\n%s", after)
	}
	if latest, _ := st.LatestSnapshot(ctx); latest.Author != "system" || !strings.Contains(latest.Reason, "172.18.0.0/16") {
		t.Errorf("snapshot %q %q", latest.Author, latest.Reason)
	}
	if changed, err := w.SyncTrustedProxies(ctx, netip.MustParsePrefix("172.18.0.0/16")); err != nil || changed {
		t.Fatalf("after the sync: changed %v, %v", changed, err)
	}
}
