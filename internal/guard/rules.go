// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"fmt"
	"strconv"
	"strings"
)

// Rule names, as written in baryovm.rules.off to switch one off for a repository.
const (
	RuleProtected   = "protected-branches"
	RuleDestructive = "destructive"
	RuleFiles       = "files"
	RuleText        = "text"
	RuleVisibility  = "visibility"
	RuleGate        = "gate"
	RuleAudit       = "audit"
)

// RuleKeyPrefix is the git config section the rule keys live in:
//
//	git config --add baryovm.rules.off files
//	git config --add baryovm.protectedBranches release
//	git config --add baryovm.forceLeaseBranches wip
//	git config baryovm.maxFileSize 5M
//	git config --add baryovm.allowFiles certs/public.pem
//	git config baryovm.visibility private
//	git config baryovm.gate scripts/gate.sh
//	git config baryovm.houseStyle true
const RuleKeyPrefix = "baryovm."

// DefaultMaxFileSize is the size limit when baryovm.maxFileSize is not set.
const DefaultMaxFileSize int64 = 10 << 20

// Rules is the per-repository setting for the rules beyond keys. The zero value has every rule on
// with the defaults.
type Rules struct {
	Off                map[string]bool
	ProtectedBranches  []string
	ForceLeaseBranches []string
	AllowFiles         []string
	MaxFileSize        int64
	Visibility         string
	Gate               string
	HouseStyle         bool
}

// On reports whether a rule is active.
func (ru Rules) On(name string) bool { return !ru.Off[name] }

// ParseRules reads `git config --get-regexp ^baryovm\.` output: one "key value" per line. Git
// lowercases key names, so keys are matched in lowercase. A bad size is ignored and the default
// stays.
func ParseRules(lines []string, offEnv string) Rules {
	ru := Rules{Off: map[string]bool{}, MaxFileSize: DefaultMaxFileSize}
	for _, n := range splitList([]string{offEnv}) {
		ru.Off[strings.ToLower(n)] = true
	}
	for _, line := range lines {
		key, val, _ := strings.Cut(strings.TrimSpace(line), " ")
		key, val = strings.ToLower(key), strings.TrimSpace(val)
		switch key {
		case "baryovm.rules.off":
			for _, n := range splitList([]string{val}) {
				ru.Off[strings.ToLower(n)] = true
			}
		case "baryovm.protectedbranches":
			ru.ProtectedBranches = append(ru.ProtectedBranches, splitList([]string{val})...)
		case "baryovm.forceleasebranches":
			ru.ForceLeaseBranches = append(ru.ForceLeaseBranches, splitList([]string{val})...)
		case "baryovm.allowfiles":
			ru.AllowFiles = append(ru.AllowFiles, splitList([]string{val})...)
		case "baryovm.maxfilesize":
			if n, ok := ParseSize(val); ok {
				ru.MaxFileSize = n
			}
		case "baryovm.visibility":
			ru.Visibility = strings.ToLower(val)
		case "baryovm.gate":
			ru.Gate = val
		case "baryovm.housestyle":
			ru.HouseStyle = val == "true" || val == "1" || val == "yes"
		}
	}
	return ru
}

// ParseSize reads 1024, 5K, 10M or 1G. Zero or less is not a size.
func ParseSize(s string) (int64, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "K"):
		mult, s = 1<<10, s[:len(s)-1]
	case strings.HasSuffix(s, "M"):
		mult, s = 1<<20, s[:len(s)-1]
	case strings.HasSuffix(s, "G"):
		mult, s = 1<<30, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * mult, true
}

// Env is what the rules need from the terminal and the shell. The wrapper fills it; tests supply
// their own.
type Env struct {
	// Interactive is true only on a terminal and not under -o json: only then may a rule ask.
	Interactive bool
	// Confirm is the value of --confirm, already removed from the command line.
	Confirm string
	// Prompt asks a question and returns the typed line.
	Prompt func(question string) (string, error)
	// Show prints what a destructive command would remove.
	Show func(lines []string)
	// RunGate runs a gate script and returns its exit code.
	RunGate func(script string) (int, error)
}

// ExtractConfirm removes `--confirm <name>` and `--confirm=<name>` from args and returns the
// value. It is consumed here and never reaches git or gh. Nothing after "--" is read.
func ExtractConfirm(args []string) (rest []string, confirm string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(rest, args[i:]...), confirm
		case a == "--confirm":
			if i+1 < len(args) {
				i++
				confirm = args[i]
			}
		case strings.HasPrefix(a, "--confirm="):
			confirm = strings.TrimPrefix(a, "--confirm=")
		default:
			rest = append(rest, a)
		}
	}
	return rest, confirm
}

// confirmTarget shows what goes and asks for the target name back. It returns nil when confirmed.
func confirmTarget(env Env, action, rule, target string, listing []string) *Verdict {
	if env.Show != nil && len(listing) > 0 {
		env.Show(listing)
	}
	how := fmt.Sprintf("Pass --confirm %s to run it without a prompt, or turn the rule off with: git config --add baryovm.rules.off %s", target, rule)
	if env.Confirm != "" {
		if env.Confirm == target {
			return nil
		}
		v := refuseRule(action, rule, target, fmt.Sprintf("%s refused: --confirm %q does not match the target %q. %s", action, env.Confirm, target, how))
		return &v
	}
	if env.Interactive && env.Prompt != nil {
		got, err := env.Prompt(fmt.Sprintf("%s is destructive. Type %s to continue: ", action, target))
		if err == nil && strings.TrimSpace(got) == target {
			return nil
		}
		v := refuseRule(action, rule, target, fmt.Sprintf("%s refused: the name typed back did not match %q", action, target))
		return &v
	}
	v := refuseRule(action, rule, target, fmt.Sprintf("%s refused: it is destructive and there is no terminal to ask. %s", action, how))
	return &v
}

func refuseRule(action, rule, target, reason string) Verdict {
	v := refuse(action, reason, nil)
	v.Rule, v.Target = rule, target
	return v
}

// SafeTarget makes a name safe to write to the activity log. It works on strings, not on a parsed
// URL, because a parse failure must not let credentials through: in every whitespace-separated
// field everything up to the last "@" before the host is dropped (URL form and the scp form
// user:pass@host:path). When the layout is unclear the field becomes a placeholder, and a value
// that looks like a key is replaced.
func SafeTarget(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	s = stripUserinfo(s)
	if len(ScanLine(s)) > 0 {
		return "[redacted]"
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// stripUserinfo works on the whole string, not per word, so a space, colon or percent sign inside
// the credentials cannot keep them. Everything up to the last "@" is dropped; when an "@" follows
// the start of a path it cannot be told from credentials, so the whole value is replaced.
func stripUserinfo(s string) string {
	at := strings.LastIndex(s, "@")
	if at < 0 {
		return s
	}
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if strings.ContainsAny(rest[:strings.LastIndex(rest, "@")], "/?#") {
			return "[redacted]"
		}
		return s[:i+3] + rest[strings.LastIndex(rest, "@")+1:]
	}
	return s[at+1:]
}

func containsStr(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
