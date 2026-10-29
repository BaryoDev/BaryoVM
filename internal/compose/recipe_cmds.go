// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package compose

// ConfigServicesCmd lists the service names the compose file declares, one per line.
func (s Stack) ConfigServicesCmd() string { return s.cmd(" config --services") }

// RemoveServiceCmd stops and removes one service's container, leaving its volumes alone.
func (s Stack) RemoveServiceCmd(svc string) string { return s.cmd(" rm -sf" + services([]string{svc})) }
