// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package provider defines the IVmProvider seam: how BaryoVM provisions a new
// VM on a cloud. Implementations (Lightsail, OCI) live in sub-packages so the
// core stays free of heavy cloud SDKs.
package provider

import "context"

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

// Provider provisions and tears down VMs on one cloud.
type Provider interface {
	// Name is the provider id, e.g. "lightsail".
	Name() string
	// Provision creates a running VM, opens 22/80/443 (to the source ranges in the Spec, anywhere by default), and returns how to reach it.
	Provision(ctx context.Context, spec Spec) (Instance, error)
	// Destroy tears down a provisioned VM by id.
	Destroy(ctx context.Context, id string) error
}
