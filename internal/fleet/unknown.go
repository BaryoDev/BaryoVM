// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package fleet

import (
	"encoding/json"
	"reflect"
	"strings"
)

// Unknown fields survive a round trip through this file.
//
// Load unmarshals fleet.json into typed structs and Save marshals them back and writes the whole
// file. Anything the struct does not have a field for is dropped by the unmarshal and then absent
// from the write, so a binary that predates a field deletes it the next time the user runs any
// command that saves. Not on the field's own command: on the next `stack add` for something else
// entirely, which is why nobody would connect the two.
//
// That is already true of every field this file has gained: releaseFile, autoUpdate, noDatabase.
// Downgrade a binary, register an unrelated stack, and the settings on every other stack are
// quietly gone. It presents later as a stack that stopped backing up, or an unattended update that
// refuses for no visible reason.
//
// Keeping the raw bytes of what we did not recognise and writing them back costs one map per entry
// and makes a downgrade lossless. It cannot repair the binaries already installed, which still drop
// what they do not know; it means every field added from here on is safe.
//
// A known field always wins over a preserved one. If a future binary adds `recipe` as a real field
// and an older file carries a stray `recipe` we preserved, the typed value is the truth.
type unknownFields map[string]json.RawMessage

// captureUnknown records every key in b that is not one of known.
func captureUnknown(b []byte, known map[string]bool) (unknownFields, error) {
	var all map[string]json.RawMessage
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	for k := range all {
		if known[k] {
			delete(all, k)
		}
	}
	if len(all) == 0 {
		return nil, nil
	}
	return all, nil
}

// mergeUnknown adds the preserved keys to an already-marshalled object, without overwriting
// anything the typed struct produced.
func mergeUnknown(b []byte, rest unknownFields) ([]byte, error) {
	if len(rest) == 0 {
		return b, nil
	}
	var known map[string]json.RawMessage
	if err := json.Unmarshal(b, &known); err != nil {
		return nil, err
	}
	for k, v := range rest {
		if _, taken := known[k]; taken {
			// The typed field is the truth. A preserved copy of a key the struct now owns is a
			// leftover from an older file, and letting it win would resurrect a stale value.
			continue
		}
		known[k] = v
	}
	return json.Marshal(known)
}

// knownKeys reads the json tags off a struct TYPE so the two lists cannot drift.
//
// Off the type, not off a marshalled value. A value's marshalling omits every empty omitempty
// field, so `port` on a VM with no port set would be absent from the derived list, captured as
// unknown, and merged back on save. An omitted-because-empty field would become a preserved stale
// one, which is worse than the bug this file exists to fix.
func knownKeys(t reflect.Type) map[string]bool {
	out := map[string]bool{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" {
			name = f.Name // encoding/json falls back to the field name
		}
		out[name] = true
	}
	return out
}

// The four methods below are the same shape twice, once per entity. The `plain` alias in each drops
// the methods, so json does not recurse into the marshaller it is already inside.

func (v *VM) UnmarshalJSON(b []byte) error {
	type plain VM
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*v = VM(p)
	rest, err := captureUnknown(b, knownKeys(reflect.TypeOf(plain{})))
	if err != nil {
		return err
	}
	v.rest = rest
	return nil
}

func (v VM) MarshalJSON() ([]byte, error) {
	type plain VM
	b, err := json.Marshal(plain(v))
	if err != nil {
		return nil, err
	}
	return mergeUnknown(b, v.rest)
}

func (s *Stack) UnmarshalJSON(b []byte) error {
	type plain Stack
	var p plain
	if err := json.Unmarshal(b, &p); err != nil {
		return err
	}
	*s = Stack(p)
	rest, err := captureUnknown(b, knownKeys(reflect.TypeOf(plain{})))
	if err != nil {
		return err
	}
	s.rest = rest
	return nil
}

func (s Stack) MarshalJSON() ([]byte, error) {
	type plain Stack
	b, err := json.Marshal(plain(s))
	if err != nil {
		return nil, err
	}
	return mergeUnknown(b, s.rest)
}
