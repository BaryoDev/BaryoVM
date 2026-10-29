// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package lightsail

import (
	"reflect"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/provider"
)

func TestPortInfosWithoutRangesSendNoCidrs(t *testing.T) {
	got := portInfos(provider.Spec{})
	if len(got) != 3 {
		t.Fatalf("got %d ports, want 3", len(got))
	}
	for i, port := range []int32{22, 80, 443} {
		if got[i].FromPort != port || got[i].ToPort != port || string(got[i].Protocol) != "tcp" {
			t.Errorf("port %d = %+v", port, got[i])
		}
		if got[i].Cidrs != nil || got[i].Ipv6Cidrs != nil {
			t.Errorf("port %d carries ranges with none set: %+v", port, got[i])
		}
	}
}

func TestPortInfosWithRangesSplitFamilies(t *testing.T) {
	got := portInfos(provider.Spec{
		SSHFrom:   []string{"203.0.113.7/32"},
		HTTPSFrom: []string{"173.245.48.0/20", "2400:cb00::/32"},
	})
	if !reflect.DeepEqual(got[0].Cidrs, []string{"203.0.113.7/32"}) || got[0].Ipv6Cidrs != nil {
		t.Errorf("22 = %+v", got[0])
	}
	if got[1].Cidrs != nil || got[1].Ipv6Cidrs != nil {
		t.Errorf("80 should be untouched: %+v", got[1])
	}
	if !reflect.DeepEqual(got[2].Cidrs, []string{"173.245.48.0/20"}) || !reflect.DeepEqual(got[2].Ipv6Cidrs, []string{"2400:cb00::/32"}) {
		t.Errorf("443 = %+v", got[2])
	}
}
