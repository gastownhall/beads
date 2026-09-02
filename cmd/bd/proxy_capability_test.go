package main

import "testing"

func TestProxyCapabilityMatrix(t *testing.T) {
	for _, cap := range []ProxyCapability{ProxyCapReadonly, ProxyCapMaxRows} {
		if err := AssertProxyCapability(ProxyModeProxied, cap); err != nil {
			t.Errorf("%s refused: %v", cap, err)
		}
	}
	for _, tc := range []struct {
		cap  ProxyCapability
		want string
	}{
		{ProxyCapWatch, "watch mode not supported in proxied-server mode"},
		{ProxyCapRepo, "--repo is not supported with --proxied-server"},
	} {
		err := AssertProxyCapability(ProxyModeProxied, tc.cap)
		if err == nil || err.Error() != tc.want {
			t.Errorf("%s error = %v, want %q", tc.cap, err, tc.want)
		}
	}
}

func TestProxyCapabilityDirectEscapeHatch(t *testing.T) {
	for _, cap := range []ProxyCapability{ProxyCapReadonly, ProxyCapMaxRows, ProxyCapWatch, ProxyCapRepo} {
		if err := AssertProxyCapability(ProxyModeDirect, cap); err != nil {
			t.Errorf("direct %s refused: %v", cap, err)
		}
	}
}
