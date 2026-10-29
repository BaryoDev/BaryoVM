// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lightsail

import (
	"github.com/BaryoDev/BaryoVM/internal/provider"
	"github.com/aws/aws-sdk-go-v2/service/lightsail/types"
)

// portInfos is the firewall request for the three ports BaryoVM opens. A port
// with no range set sends no CIDR fields at all, so Lightsail applies its own
// default and an unrestricted spec produces the request it always did. IPv4
// ranges go in Cidrs and IPv6 ranges in Ipv6Cidrs, which is how the API splits
// them (Cloudflare publishes both). The PortInfo docs do not say an empty
// family is closed, so a limited port must name both families (CheckFamilies).
func portInfos(spec provider.Spec) ([]types.PortInfo, error) {
	if err := spec.CheckFamilies(); err != nil {
		return nil, err
	}
	var out []types.PortInfo
	for _, port := range []int32{22, 80, 443} {
		pi := types.PortInfo{FromPort: port, ToPort: port, Protocol: types.NetworkProtocolTcp}
		if spec.Limited(int(port)) {
			pi.Cidrs, pi.Ipv6Cidrs = provider.SplitFamilies(spec.CidrsFor(int(port)))
		}
		out = append(out, pi)
	}
	return out, nil
}
