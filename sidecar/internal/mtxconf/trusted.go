package mtxconf

import (
	"context"
	"errors"
	"net/netip"
	"slices"

	"go.yaml.in/yaml/v3"

	"mtxui/internal/yamledit"
)

// trustedProxyKeys are MediaMTX's settings naming the proxies it believes X-Forwarded-For from: the sidecar, which
// proxies HLS, WebRTC signalling and playback for browsers.
var trustedProxyKeys = []string{"hlsTrustedProxies", "webrtcTrustedProxies", "playbackTrustedProxies"}

// SyncTrustedProxies sets MediaMTX's trusted proxies to the stack's subnet where they differ. Setup writes them once,
// but the subnet can change (the public compose file lets Docker choose it, and a recreated network may get another),
// and MediaMTX would then see every proxied browser as the sidecar. It reports whether it wrote; an invalid subnet
// (not detected) changes nothing.
func (w *Writer) SyncTrustedProxies(ctx context.Context, subnet netip.Prefix) (bool, error) {
	if !subnet.IsValid() {
		return false, nil
	}
	cur, _, err := w.Current()
	if err != nil {
		return false, err
	}
	var conf map[string]any
	if err := yaml.Unmarshal(cur, &conf); err != nil {
		return false, err
	}
	want := []any{subnet.Masked().String()}
	var stale []string
	for _, k := range trustedProxyKeys {
		if got, _ := conf[k].([]any); !slices.Equal(got, want) {
			stale = append(stale, k)
		}
	}
	if len(stale) == 0 {
		return false, nil
	}
	_, err = w.Edit(ctx, "system", "trusted proxies follow the stack subnet "+subnet.Masked().String(), nil, func(d *yamledit.Doc) error {
		for _, k := range stale {
			if err := d.Set([]string{k}, []string{subnet.Masked().String()}); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, ErrUnchanged) {
		return false, nil
	}
	return err == nil, err
}
