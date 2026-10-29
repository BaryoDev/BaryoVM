// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/BaryoDev/BaryoVM/internal/guard"
)

// withPublishSeams fakes npm pack with the given output and records the commands it was asked to run.
func withPublishSeams(t *testing.T, packOut string, packErr error) *[]string {
	t.Helper()
	old := publishExec
	var ran []string
	testNpmCfg = map[string]string{"registry": "https://registry.npmjs.org/"}
	publishExec = func(dir, name string, args []string) (string, error) {
		ran = append(ran, dir+": "+name+" "+strings.Join(args, " "))
		if len(args) >= 3 && args[0] == "config" && args[1] == "get" {
			v, ok := testNpmCfg[args[2]]
			if !ok {
				return "undefined\n", nil
			}
			if v == "FAIL" {
				return "", errFake("npm config broke")
			}
			return v + "\n", nil
		}
		return packOut, packErr
	}
	t.Cleanup(func() { publishExec = old })
	t.Setenv("BARYOVM_ALLOWED_REGISTRIES", "")
	return &ran
}

// testNpmCfg is what the fake npm config get answers, by key.
var testNpmCfg map[string]string

const packOne = `[{"name":"demo","files":[{"path":"package.json","size":40},{"path":"index.js","size":20}]}]`

func npmFiles(index string) map[string]string {
	return map[string]string{"package.json": `{"name":"demo","version":"1.0.0"}`, "index.js": index}
}

func nupkg(t *testing.T, entries map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func wantRefused(t *testing.T, err error, ran *[]string, ranBefore int, contains ...string) {
	t.Helper()
	if code, _ := exitCodeOf(err); err == nil || code != 1 {
		t.Fatalf("want exit 1, got %v", err)
	}
	if len(*ran) != ranBefore {
		t.Fatalf("the real tool ran after a refusal: %v", *ran)
	}
	for _, c := range contains {
		if !strings.Contains(err.Error(), c) {
			t.Fatalf("error %q lacks %q", err.Error(), c)
		}
	}
	if strings.Contains(err.Error(), cliFakeKey) {
		t.Fatalf("the key leaked into the error")
	}
}

func TestPublishNpmWithKeyIsRefused(t *testing.T) {
	packs := withPublishSeams(t, packOne, nil)
	ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("const k = \"" + cliFakeKey + "\"\n")}, 0)
	err := runGroup(t, "publish", "npm", "--access", "public")
	wantRefused(t, err, ran, 0, "index.js:1")
	if len(*packs) < 1 || !strings.Contains((*packs)[0], "npm pack --dry-run --json") {
		t.Fatalf("pack: %v", *packs)
	}
}

func TestPublishNpmCleanPackagePassesThroughWithArgsUnchanged(t *testing.T) {
	withPublishSeams(t, packOne, nil)
	ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("module.exports = 1\n")}, 0)
	if err := runGroup(t, "publish", "npm", "--access", "public", "--otp", "123456"); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 1 || (*ran)[0] != "npm publish --access public --otp 123456" {
		t.Fatalf("ran %v", *ran)
	}
}

func TestPublishNpmPrivatePackageToPublicRegistryIsRefused(t *testing.T) {
	withPublishSeams(t, packOne, nil)
	files := npmFiles("x\n")
	files["package.json"] = `{"name":"demo","private":true}`
	ran := withGuardSeams(t, &fakeGuardRunner{files: files}, 0)
	err := runGroup(t, "publish", "npm")
	wantRefused(t, err, ran, 0, "registry.npmjs.org", "private")
}

func TestPublishNpmRegistryOutsidePolicyIsRefusedAndNamed(t *testing.T) {
	withPublishSeams(t, packOne, nil)
	t.Setenv("BARYOVM_ALLOWED_REGISTRIES", "pkgs.corp.example")
	ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("x\n")}, 0)
	err := runGroup(t, "publish", "npm")
	wantRefused(t, err, ran, 0, "registry.npmjs.org", "baryovm.allowedRegistries")
}

func TestPublishNpmRegistryComesFromNpmConfig(t *testing.T) {
	t.Run("npm says allowed", func(t *testing.T) {
		withPublishSeams(t, packOne, nil)
		testNpmCfg["registry"] = "https://pkgs.corp.example/npm/"
		t.Setenv("BARYOVM_ALLOWED_REGISTRIES", "pkgs.corp.example")
		ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("x\n")}, 0)
		if err := runGroup(t, "publish", "npm"); err != nil || len(*ran) != 1 {
			t.Fatalf("err %v ran %v", err, *ran)
		}
	})
	t.Run("scoped registry from npm", func(t *testing.T) {
		withPublishSeams(t, packOne, nil)
		testNpmCfg["@acme:registry"] = "https://pkgs.corp.example/npm/"
		t.Setenv("BARYOVM_ALLOWED_REGISTRIES", "pkgs.corp.example")
		files := npmFiles("x\n")
		files["package.json"] = `{"name":"@acme/demo"}`
		ran := withGuardSeams(t, &fakeGuardRunner{files: files}, 0)
		if err := runGroup(t, "publish", "npm"); err != nil || len(*ran) != 1 {
			t.Fatalf("err %v ran %v", err, *ran)
		}
	})
	t.Run("npm outranks a project .npmrc that says otherwise", func(t *testing.T) {
		withPublishSeams(t, packOne, nil)
		testNpmCfg["registry"] = "https://evil.example/"
		t.Setenv("BARYOVM_ALLOWED_REGISTRIES", "pkgs.corp.example")
		files := npmFiles("x\n")
		files[".npmrc"] = "registry=https://pkgs.corp.example/\n"
		ran := withGuardSeams(t, &fakeGuardRunner{files: files}, 0)
		wantRefused(t, runGroup(t, "publish", "npm"), ran, 0, "evil.example")
	})
	t.Run("publishConfig wins over npm", func(t *testing.T) {
		withPublishSeams(t, packOne, nil)
		testNpmCfg["registry"] = "https://pkgs.corp.example/"
		t.Setenv("BARYOVM_ALLOWED_REGISTRIES", "pkgs.corp.example")
		files := map[string]string{"package.json": `{"name":"demo","publishConfig":{"registry":"https://evil.example/"}}`, "index.js": "x\n"}
		ran := withGuardSeams(t, &fakeGuardRunner{files: files}, 0)
		wantRefused(t, runGroup(t, "publish", "npm"), ran, 0, "evil.example")
	})
	t.Run("npm config get failing refuses", func(t *testing.T) {
		withPublishSeams(t, packOne, nil)
		testNpmCfg["registry"] = "FAIL"
		ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("x\n")}, 0)
		wantRefused(t, runGroup(t, "publish", "npm"), ran, 0, "npm config get registry")
	})
	t.Run("an answer that is not a URL refuses", func(t *testing.T) {
		withPublishSeams(t, packOne, nil)
		testNpmCfg["registry"] = "not-a-url"
		ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("x\n")}, 0)
		wantRefused(t, runGroup(t, "publish", "npm"), ran, 0, "not a readable URL")
	})
	t.Run("an abbreviated registry flag is refused under a policy", func(t *testing.T) {
		packs := withPublishSeams(t, packOne, nil)
		old := publishExec
		publishExec = func(dir, name string, args []string) (string, error) {
			if len(args) >= 3 && args[0] == "config" {
				for _, a := range args[3:] {
					if v, ok := strings.CutPrefix(a, "--reg="); ok {
						return v + "\n", nil
					}
				}
			}
			return old(dir, name, args)
		}
		t.Setenv("BARYOVM_ALLOWED_REGISTRIES", "pkgs.corp.example")
		ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("x\n")}, 0)
		wantRefused(t, runGroup(t, "publish", "npm", "--reg=https://evil.example"), ran, 0, "evil.example")
		_ = packs
	})
	t.Run("config get runs in the process cwd and gets unknown flags", func(t *testing.T) {
		packs := withPublishSeams(t, packOne, nil)
		files := map[string]string{"sub/package.json": `{"name":"demo"}`, "sub/index.js": "x\n"}
		withGuardSeams(t, &fakeGuardRunner{files: files}, 0)
		if err := runGroup(t, "publish", "npm", "sub", "--frobnicate"); err != nil {
			t.Fatal(err)
		}
		var cfgCall string
		for _, c := range *packs {
			if strings.Contains(c, "config get registry") {
				cfgCall = c
			}
		}
		if cfgCall != ": npm config get registry --frobnicate" {
			t.Fatalf("calls %v", *packs)
		}
	})
	t.Run("config flags reach npm config get", func(t *testing.T) {
		packs := withPublishSeams(t, packOne, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("x\n")}, 0)
		if err := runGroup(t, "publish", "npm", "--userconfig", "/u/.npmrc", "--tag", "next"); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range *packs {
			if strings.Contains(c, "config get registry --userconfig /u/.npmrc --tag next") {
				found = true
			}
		}
		if !found || (*ran)[0] != "npm publish --userconfig /u/.npmrc --tag next" {
			t.Fatalf("calls %v ran %v", *packs, *ran)
		}
	})
}

func TestPublishNpmMalformedPackJSONIsRefused(t *testing.T) {
	withPublishSeams(t, "npm WARN something\nnot json", nil)
	ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("x\n")}, 0)
	wantRefused(t, runGroup(t, "publish", "npm"), ran, 0, "could not be read")
}

func TestPublishNpmPackFailureIsRefused(t *testing.T) {
	withPublishSeams(t, "", errFake("pack broke"))
	ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("x\n")}, 0)
	wantRefused(t, runGroup(t, "publish", "npm"), ran, 0, "pack broke")
}

func TestPublishNpmPackOutputAfterScriptNoiseIsRead(t *testing.T) {
	withPublishSeams(t, "> demo@1.0.0 prepack\n"+packOne, nil)
	ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("x\n")}, 0)
	if err := runGroup(t, "publish", "npm"); err != nil || len(*ran) != 1 {
		t.Fatalf("err %v ran %v", err, *ran)
	}
}

func TestPublishNpmTarballAndWorkspacesAreRefused(t *testing.T) {
	withPublishSeams(t, packOne, nil)
	ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("x\n")}, 0)
	wantRefused(t, runGroup(t, "publish", "npm", "demo-1.0.0.tgz"), ran, 0, "tarball")
	wantRefused(t, runGroup(t, "publish", "npm", "-w", "packages/a"), ran, 0, "workspace")
}

func TestPublishNpmPolicyThatCannotBeReadIsRefused(t *testing.T) {
	withPublishSeams(t, packOne, nil)
	ran := withGuardSeams(t, &fakeGuardRunner{files: npmFiles("x\n"), errs: map[string]error{"config --get-all": codedErr{code: 128}}}, 0)
	wantRefused(t, runGroup(t, "publish", "npm"), ran, 0, "cannot read baryovm.allowedRegistries")
}

func TestPublishNuGetWithKeyIsRefused(t *testing.T) {
	pkg := nupkg(t, map[string]string{"App.nuspec": "<package/>", "content/cfg.json": "{\"k\":\"" + cliFakeKey + "\"}\n"})
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"App.1.0.0.nupkg": pkg}}, 0)
	err := runGroup(t, "publish", "nuget", "App.1.0.0.nupkg", "-k", "x")
	wantRefused(t, err, ran, 0, "App.1.0.0.nupkg:content/cfg.json:1")
}

func TestPublishNuGetCleanPackagePassesThroughWithArgsUnchanged(t *testing.T) {
	pkg := nupkg(t, map[string]string{"App.nuspec": "<package/>"})
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"App.1.0.0.nupkg": pkg}}, 0)
	t.Setenv("BARYOVM_ALLOWED_REGISTRIES", "")
	if err := runGroup(t, "publish", "nuget", "App.1.0.0.nupkg", "-s", "https://api.nuget.org/v3/index.json", "--skip-duplicate"); err != nil {
		t.Fatal(err)
	}
	want := "dotnet nuget push App.1.0.0.nupkg -s https://api.nuget.org/v3/index.json --skip-duplicate"
	if len(*ran) != 1 || (*ran)[0] != want {
		t.Fatalf("ran %v", *ran)
	}
}

func TestPublishNuGetSourceOutsidePolicyIsRefused(t *testing.T) {
	pkg := nupkg(t, map[string]string{"App.nuspec": "<package/>"})
	t.Setenv("BARYOVM_ALLOWED_REGISTRIES", "pkgs.dev.azure.com")
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"App.1.0.0.nupkg": pkg}}, 0)
	wantRefused(t, runGroup(t, "publish", "nuget", "App.1.0.0.nupkg", "-s", "https://api.nuget.org/v3/index.json", "-n"), ran, 0, "api.nuget.org")
	wantRefused(t, runGroup(t, "publish", "nuget", "App.1.0.0.nupkg", "--source", "feedname", "-n"), ran, 0, "feedname")
	if err := runGroup(t, "publish", "nuget", "App.1.0.0.nupkg", "-n", "-s", "https://pkgs.dev.azure.com/o/_packaging/f/nuget/v3/index.json"); err != nil {
		t.Fatal(err)
	}
}

func TestPublishNuGetPolicyNeedsExplicitSourceAndSymbols(t *testing.T) {
	pkg := nupkg(t, map[string]string{"App.nuspec": "<package/>"})
	t.Setenv("BARYOVM_ALLOWED_REGISTRIES", "pkgs.corp.example")
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"App.nupkg": pkg}}, 0)
	feed := "https://pkgs.corp.example/nuget/v3/index.json"
	wantRefused(t, runGroup(t, "publish", "nuget", "App.nupkg", "--no-symbols"), ran, 0, "no --source")
	wantRefused(t, runGroup(t, "publish", "nuget", "App.nupkg", "-s", feed, "--no-symbols", "--configfile", "n.config"), ran, 0, "--configfile")
	wantRefused(t, runGroup(t, "publish", "nuget", "App.nupkg", "-s", feed), ran, 0, "symbol")
	wantRefused(t, runGroup(t, "publish", "nuget", "App.nupkg", "-s", feed, "-ss", "https://evil.example/s"), ran, 0, "evil.example")
	if err := runGroup(t, "publish", "nuget", "App.nupkg", "-s", feed, "-n"); err != nil {
		t.Fatal(err)
	}
	if err := runGroup(t, "publish", "nuget", "App.nupkg", "-s", feed, "-ss", feed); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 2 {
		t.Fatalf("ran %v", *ran)
	}
}

func TestPublishNuGetOversizedEntryIsRefused(t *testing.T) {
	old := guard.MaxEntryBytes
	guard.MaxEntryBytes = 64
	t.Cleanup(func() { guard.MaxEntryBytes = old })
	pkg := nupkg(t, map[string]string{"lib/big.dll": strings.Repeat("a", 200)})
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"App.1.0.0.nupkg": pkg}}, 0)
	wantRefused(t, runGroup(t, "publish", "nuget", "App.1.0.0.nupkg"), ran, 0, "lib/big.dll", "limit")
}

func TestPublishNuGetUnreadablePackageIsRefused(t *testing.T) {
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"App.nupkg": "not a zip"}}, 0)
	wantRefused(t, runGroup(t, "publish", "nuget", "App.nupkg"), ran, 0, "not a readable package")
}

type errFake string

func (e errFake) Error() string { return string(e) }
