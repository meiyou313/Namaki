// SPDX-License-Identifier: GPL-3.0-or-later
package mobile

import (
	"errors"

	"foxvpn/android/core/mobile/signinproxy"
)

// openSignInProxy starts the browser sign-in's proxy when it does not run
// (on preferredPort if it can), arms it, and returns its address
// ("127.0.0.1:port"). The proxy dials through the core's own dialer, so
// the browser view reaches Mozilla's sign-in pages the way the core's
// account requests do, with no account and the VPN off.
func (b *Backend) openSignInProxy(preferredPort int) (string, error) {
	if b.dialer == nil {
		return "", errors.New("the core's dialer is not ready")
	}
	b.signinProxyMu.Lock()
	defer b.signinProxyMu.Unlock()
	if b.signinProxy == nil {
		server, err := signinproxy.Listen(preferredPort, b.dialer.DialContext)
		if err != nil {
			return "", err
		}
		b.signinProxy = server
	}
	b.signinProxy.Arm()
	return b.signinProxy.Addr(), nil
}

// closeSignInProxy disarms the proxy: the sign-in page closed.
func (b *Backend) closeSignInProxy() {
	b.signinProxyMu.Lock()
	defer b.signinProxyMu.Unlock()
	if b.signinProxy != nil {
		b.signinProxy.Disarm()
	}
}
