// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package guard

import (
	"fmt"
	"strings"
)

// ghWords returns the group and verb of a gh command and the index of the verb in args. -R and
// --repo take a value that is not a word.
func ghWords(args []string) (words []string, verbIdx int) {
	for i := 0; i < len(args) && len(words) < 2; i++ {
		a := args[i]
		switch {
		case a == "-R" || a == "--repo":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			words = append(words, a)
			verbIdx = i
		}
	}
	return words, verbIdx
}

// ghPositional is the first argument after the verb that is not a flag or a flag's value.
func ghPositional(args []string, verbIdx int) string {
	for i := verbIdx + 1; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-R" || a == "--repo":
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return a
		}
	}
	return ""
}

// CheckGhRules runs the key scan, then the rules that apply to the gh command: AI attribution in
// the text it publishes, destructive deletes, and the visibility of a new repository.
func CheckGhRules(r Runner, args []string, ru Rules, env Env) Verdict {
	var texts []namedText
	v := checkGh(r, args, func(n, t string) { texts = append(texts, namedText{n, t}) })
	words, verbIdx := ghWords(args)
	key := strings.Join(words, " ")
	action := "gh " + key
	guarded := ghChecked[key] || key == "repo create" || key == "repo delete" || key == "release delete"
	if v.Allowed && ru.On(RuleDestructive) && len(words) > 0 {
		if rv := ghBypass(args, words); rv != nil {
			v = *rv
			guarded = true
		}
	}
	if v.Allowed && len(words) == 2 {
		if ghChecked[key] && ru.On(RuleText) {
			for _, t := range texts {
				if fs := CheckText(ru, t.name, t.text, false); len(fs) > 0 {
					v = textRefusal(action, ru, fs)
					break
				}
			}
		}
		switch key {
		case "repo create":
			if ru.On(RuleVisibility) {
				if rv := ghVisibility(args, ru); rv != nil {
					v = *rv
				}
			}
		case "repo delete", "release delete":
			if ru.On(RuleDestructive) {
				v = ghDelete(args, verbIdx, key, ru, env, v)
			}
		}
	}
	if v.Target == "" && len(words) == 2 {
		v.Target = SafeTarget(ghPositional(args, verbIdx))
	}
	v.Audit = ru.On(RuleAudit) && (!v.Allowed || guarded)
	if v.Args == nil {
		v.Args = args
	}
	return v
}

func ghVisibility(args []string, ru Rules) *Verdict {
	const action = "gh repo create"
	chosen := ""
	for _, a := range args {
		if a == "--" {
			break
		}
		switch a {
		case "--public", "--private", "--internal":
			chosen = strings.TrimPrefix(a, "--")
		}
	}
	off := "or turn the rule off with: git config --add baryovm.rules.off " + RuleVisibility
	if chosen == "" {
		hint := "Pass --public, --private or --internal"
		if ru.Visibility != "" {
			hint = "The policy names " + ru.Visibility + ": pass --" + ru.Visibility
		}
		v := refuseRule(action, RuleVisibility, "", fmt.Sprintf("%s refused: no visibility given and there is no default. %s, %s", action, hint, off))
		return &v
	}
	if ru.Visibility != "" && chosen != ru.Visibility {
		v := refuseRule(action, RuleVisibility, chosen, fmt.Sprintf("%s refused: --%s but the policy names %s. Pass --%s, change the policy with: git config baryovm.visibility %s, %s", action, chosen, ru.Visibility, ru.Visibility, chosen, off))
		return &v
	}
	return nil
}

func ghDelete(args []string, verbIdx int, key string, ru Rules, env Env, ok Verdict) Verdict {
	action := "gh " + key
	target := ghPositional(args, verbIdx)
	if target == "" {
		what := "the release tag"
		if key == "repo delete" {
			what = "the repository as OWNER/REPO"
		}
		return refuseRule(action, RuleDestructive, "", fmt.Sprintf("%s refused: name %s so it can be confirmed", action, what))
	}
	var listing []string
	if key == "repo delete" {
		listing = []string{"the repository " + target + " with its issues, pull requests, releases and wiki will be deleted"}
	} else {
		listing = []string{"the release " + target + " will be deleted"}
	}
	if v := confirmTarget(env, action, RuleDestructive, target, listing); v != nil {
		return *v
	}
	ok.Target = SafeTarget(target)
	// gh asks again on its own, or fails with no terminal; the name was confirmed here.
	out := append([]string{}, args...)
	for _, a := range out {
		if a == "--yes" || a == "-y" {
			ok.Args = out
			return ok
		}
	}
	ok.Args = append(out, "--yes")
	return ok
}

// ghBypass refuses the gh commands that would run something the guard cannot read: an alias that
// can stand for any command, and an API call that changes data (a delete, or a call with fields,
// which gh turns into a POST).
func ghBypass(args, words []string) *Verdict {
	off := "or turn the rule off with: git config --add baryovm.rules.off " + RuleDestructive
	switch {
	case words[0] == "alias" && len(words) > 1 && (words[1] == "set" || words[1] == "import"):
		action := "gh alias " + words[1]
		v := refuseRule(action, RuleDestructive, "", fmt.Sprintf("%s refused: a gh alias can stand for a command the guard would refuse, such as repo delete. Run the gh command directly, %s", action, off))
		return &v
	case words[0] == "api":
		method, fields := "", false
		for i := 0; i < len(args); i++ {
			a := args[i]
			switch {
			case a == "--":
				i = len(args)
			case a == "-X" || a == "--method":
				if i+1 < len(args) {
					i++
					method = args[i]
				}
			case strings.HasPrefix(a, "--method="):
				method = strings.TrimPrefix(a, "--method=")
			case strings.HasPrefix(a, "-X") && !strings.HasPrefix(a, "--"):
				method = strings.TrimPrefix(a, "-X")
			case a == "--field" || a == "--raw-field" || a == "--input" || strings.HasPrefix(a, "--field=") || strings.HasPrefix(a, "--raw-field=") || strings.HasPrefix(a, "--input="):
				fields = true
			case strings.HasPrefix(a, "-f") || strings.HasPrefix(a, "-F"):
				fields = true
			}
		}
		if (method != "" && !strings.EqualFold(method, "GET")) || (method == "" && fields) {
			v := refuseRule("gh api", RuleDestructive, "", "gh api refused: it would change data (a method other than GET, or fields, which make gh send a POST). Pass -X GET for a read, run the change with a gh command that has its own check, "+off)
			return &v
		}
	}
	return nil
}
