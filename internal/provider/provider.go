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
}

// Provider provisions and tears down VMs on one cloud.
type Provider interface {
	// Name is the provider id, e.g. "lightsail".
	Name() string
	// Provision creates a running VM, opens 22/80/443, and returns how to reach it.
	Provision(ctx context.Context, spec Spec) (Instance, error)
	// Destroy tears down a provisioned VM by id.
	Destroy(ctx context.Context, id string) error
}
