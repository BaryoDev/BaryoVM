// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package provider defines the IVmProvider seam: how BaryoVM provisions a new
// VM on a cloud. Implementations (Lightsail, OCI) live in sub-packages so the
// core stays free of heavy cloud SDKs.
package provider

import (
	"context"
	"fmt"
	"net"
)

// Instance is a provisioned VM as BaryoVM sees it.
type Instance struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PublicIP string `json:"publicIp"`
	User     string `json:"user"`     // default SSH user, e.g. "ubuntu" or "opc"
	Provider string `json:"provider"` // "lightsail" | "oci"
}

// Spec describes a VM to create.
type Spec struct {
	Name      string `json:"name"`      // instance + resource name
	Region    string `json:"region"`    // provider region / availability domain
	Size      string `json:"size"`      // provider bundle/shape id
	PublicKey string `json:"publicKey"` // SSH public key material to authorize

	// Source ranges allowed to reach each port. Unset (nil) keeps the old
	// behaviour: open to 0.0.0.0/0. Use CidrsFor rather than reading these.
	SSHFrom   []string `json:"sshFrom,omitempty"`   // port 22
	HTTPFrom  []string `json:"httpFrom,omitempty"`  // port 80
	HTTPSFrom []string `json:"httpsFrom,omitempty"` // port 443
}

// AnyIPv4 is what a port with no source limit allows.
const AnyIPv4 = "0.0.0.0/0"

// CidrsFor returns the source ranges for a port, or 0.0.0.0/0 when none were
// set, so a zero Spec behaves exactly as it did before ranges existed.
func (s Spec) CidrsFor(port int) []string {
	var set []string
	switch port {
	case 22:
		set = s.SSHFrom
	case 80:
		set = s.HTTPFrom
	case 443:
		set = s.HTTPSFrom
	}
	if len(set) == 0 {
		return []string{AnyIPv4}
	}
	return set
}

// Limited reports whether any range was set for the port.
func (s Spec) Limited(port int) bool {
	return len(s.CidrsFor(port)) != 1 || s.CidrsFor(port)[0] != AnyIPv4
}

// SplitFamilies separates ranges into IPv4 and IPv6, the way Lightsail takes them.
func SplitFamilies(cidrs []string) (v4, v6 []string) {
	for _, c := range cidrs {
		ip, _, err := net.ParseCIDR(c)
		if err == nil && ip.To4() == nil {
			v6 = append(v6, c)
		} else {
			v4 = append(v4, c)
		}
	}
	return v4, v6
}

// CheckFamilies refuses a limited port that names only one IP family. The
// Lightsail PortInfo docs do not say that an empty cidrs or ipv6Cidrs list
// means closed, so the other family could stay open to everyone. To close a
// family on purpose, name a range in it that reaches nothing (::1/128 or
// 127.0.0.1/32).
func (s Spec) CheckFamilies() error {
	for _, port := range []int{22, 80, 443} {
		if !s.Limited(port) {
			continue
		}
		v4, v6 := SplitFamilies(s.CidrsFor(port))
		switch {
		case len(v6) == 0:
			return fmt.Errorf("port %d has IPv4 ranges but no IPv6 range, and Lightsail does not document an empty IPv6 list as closed: add an IPv6 range, or ::1/128 to close IPv6", port)
		case len(v4) == 0:
			return fmt.Errorf("port %d has IPv6 ranges but no IPv4 range, and Lightsail does not document an empty IPv4 list as closed: add an IPv4 range, or 127.0.0.1/32 to close IPv4", port)
		}
	}
	return nil
}

// Provider provisions and tears down VMs on one cloud.
type Provider interface {
	// Name is the provider id, e.g. "lightsail".
	Name() string
	// Provision creates a running VM, opens 22/80/443 (to the source ranges in the Spec, anywhere by default), and returns how to reach it.
	Provision(ctx context.Context, spec Spec) (Instance, error)
	// Destroy tears down a provisioned VM by id.
	Destroy(ctx context.Context, id string) error
}
