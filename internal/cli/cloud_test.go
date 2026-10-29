// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package cli

import (
	"errors"
	"strings"
	"testing"
)

type fakeCloudFS struct {
	lists  map[string][]string
	exists map[string]bool
}

func (f fakeCloudFS) ListFiles(dir string) ([]string, error) {
	l, ok := f.lists[dir]
	if !ok {
		return nil, errors.New("no such directory")
	}
	return l, nil
}
func (f fakeCloudFS) Exists(p string) bool   { return f.exists[p] }
func (f fakeCloudFS) Getwd() (string, error) { return "/work/app", nil }

// withCloudSeams fakes the file system and the cloud CLI. It records every cloud CLI call.
func withCloudSeams(t *testing.T, fsys fakeCloudFS, execErr error) *[]string {
	t.Helper()
	oldE, oldF, oldT := cloudExec, cloudFS, cloudTempDir
	var calls []string
	cloudExec = func(dir, name string, args []string) (string, error) {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return "", execErr
	}
	cloudFS = fsys
	cloudTempDir = func() (string, func(), error) { return "/out", func() {}, nil }
	t.Cleanup(func() { cloudExec, cloudFS, cloudTempDir = oldE, oldF, oldT })
	t.Setenv("BARYOVM_ALLOWED_CLOUD_PROJECTS", "")
	return &calls
}

func TestCloudWranglerKeyInVarsIsRefusedAndNamesSecretPut(t *testing.T) {
	calls := withCloudSeams(t, fakeCloudFS{}, nil)
	toml := "name = \"w\"\n[vars]\nAPI = \"" + cliFakeKey + "\"\n"
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"wrangler.toml": toml}}, 0)
	err := runGroup(t, "cloud", "wrangler", "deploy")
	wantRefused(t, err, ran, 0, "wrangler.toml:3", "wrangler secret put")
	if len(*calls) != 0 {
		t.Fatalf("the dry run started after a key in vars: %v", *calls)
	}
}

func TestCloudWranglerKeyInBundleIsRefused(t *testing.T) {
	calls := withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"/out": {"index.js"}}}, nil)
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{
		"wrangler.toml": "name = \"w\"\n",
		"/out/index.js": "var k = \"" + cliFakeKey + "\";\n",
	}}, 0)
	err := runGroup(t, "cloud", "wrangler", "deploy", "--env", "prod")
	wantRefused(t, err, ran, 0, "/out/index.js:1", "Worker bundle")
	if len(*calls) != 1 || (*calls)[0] != "wrangler deploy --env prod --dry-run --outdir /out" {
		t.Fatalf("dry run: %v", *calls)
	}
}

func TestCloudWranglerKeyAsVarArgumentIsRefused(t *testing.T) {
	withCloudSeams(t, fakeCloudFS{}, nil)
	ran := withGuardSeams(t, &fakeGuardRunner{}, 0)
	err := runGroup(t, "cloud", "wrangler", "deploy", "--var", "K:"+cliFakeKey)
	wantRefused(t, err, ran, 0, "argument 3", "wrangler secret put")
}

func TestCloudWranglerCleanDeployPassesThroughUnchanged(t *testing.T) {
	withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"/out": {"index.js"}}}, nil)
	ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"wrangler.toml": "name = \"w\"\n", "/out/index.js": "export default {}\n"}}, 0)
	if err := runGroup(t, "cloud", "wrangler", "deploy", "--env", "prod"); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 1 || (*ran)[0] != "wrangler deploy --env prod" {
		t.Fatalf("ran %v", *ran)
	}
}

func TestCloudWranglerFailsClosed(t *testing.T) {
	t.Run("dry run fails", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{}, errors.New("build failed"))
		ran := withGuardSeams(t, &fakeGuardRunner{}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy"), ran, 0, "dry-run failed")
	})
	t.Run("dry run wrote nothing", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"/out": {}}}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy"), ran, 0, "no bundle")
	})
	t.Run("a named config that cannot be read", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy", "-c", "other.toml"), ran, 0, "other.toml")
	})
	t.Run("leading option", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "--env", "prod", "deploy"), ran, 0, "after the command")
	})
}

func TestCloudWranglerSecretPutPassesThrough(t *testing.T) {
	calls := withCloudSeams(t, fakeCloudFS{}, nil)
	ran := withGuardSeams(t, &fakeGuardRunner{}, 0)
	if err := runGroup(t, "cloud", "wrangler", "secret", "put", "API"); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 1 || len(*calls) != 0 {
		t.Fatalf("ran %v dry runs %v", *ran, *calls)
	}
}

const fbJSON = `{"hosting":{"public":"dist","ignore":["firebase.json","**/.*"]}}`
const fbRC = `{"projects":{"default":"app-prod","staging":"app-stage"}}`

func fbSeams(t *testing.T, extra map[string]string, answers map[string]string, listing []string) *[]string {
	t.Helper()
	withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"dist": listing}}, nil)
	files := map[string]string{"firebase.json": fbJSON, ".firebaserc": fbRC}
	for k, v := range extra {
		files[k] = v
	}
	return withGuardSeams(t, &fakeGuardRunner{files: files, answers: answers}, 0)
}

func TestCloudFirebaseKeyInDeployedFileIsRefused(t *testing.T) {
	ran := fbSeams(t, map[string]string{"dist/app.js": "const k = \"" + cliFakeKey + "\"\n"}, nil, []string{"app.js"})
	err := runGroup(t, "cloud", "firebase", "deploy", "--only", "hosting")
	wantRefused(t, err, ran, 0, "dist/app.js:1", "firebase would deploy")
}

func TestCloudFirebaseIgnoredFileIsNotScanned(t *testing.T) {
	ran := fbSeams(t, map[string]string{"dist/.env": "K=" + cliFakeKey + "\n", "dist/index.html": "hi\n"}, nil, []string{".env", "index.html"})
	if err := runGroup(t, "cloud", "firebase", "deploy"); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 1 || (*ran)[0] != "firebase deploy" {
		t.Fatalf("ran %v", *ran)
	}
}

func TestCloudFirebaseProjectOutsidePolicyIsRefusedBeforeDeploy(t *testing.T) {
	ran := fbSeams(t, map[string]string{"dist/index.html": "hi\n"}, nil, []string{"index.html"})
	t.Setenv("BARYOVM_ALLOWED_CLOUD_PROJECTS", "app-stage")
	err := runGroup(t, "cloud", "firebase", "deploy", "--project", "app-prod")
	wantRefused(t, err, ran, 0, "app-prod", "baryovm.allowedCloudProjects", "app-stage")
}

func TestCloudFirebaseProjectPolicyFromGitConfig(t *testing.T) {
	ran := fbSeams(t, map[string]string{"dist/index.html": "hi\n"}, map[string]string{"config --get-all baryovm.allowedCloudProjects": "other-app\n"}, []string{"index.html"})
	err := runGroup(t, "cloud", "firebase", "deploy", "-P", "app-prod")
	wantRefused(t, err, ran, 0, "app-prod", "other-app")
}

func TestCloudFirebaseProjectResolution(t *testing.T) {
	t.Run("alias from .firebaserc is allowed by its id", func(t *testing.T) {
		ran := fbSeams(t, map[string]string{"dist/index.html": "hi\n"}, nil, []string{"index.html"})
		t.Setenv("BARYOVM_ALLOWED_CLOUD_PROJECTS", "app-stage")
		if err := runGroup(t, "cloud", "firebase", "deploy", "--project=staging"); err != nil || len(*ran) != 1 {
			t.Fatalf("err %v ran %v", err, *ran)
		}
	})
	t.Run("a policy without --project refuses", func(t *testing.T) {
		ran := fbSeams(t, map[string]string{"dist/index.html": "hi\n"}, nil, []string{"index.html"})
		t.Setenv("BARYOVM_ALLOWED_CLOUD_PROJECTS", "app-prod")
		wantRefused(t, runGroup(t, "cloud", "firebase", "deploy"), ran, 0, "--project")
	})
	t.Run("no project anywhere refuses", func(t *testing.T) {
		ran := fbSeams(t, map[string]string{".firebaserc": "{}"}, nil, []string{"index.html"})
		wantRefused(t, runGroup(t, "cloud", "firebase", "deploy"), ran, 0, "cannot be determined")
	})
	t.Run("an unreadable firebase.json refuses", func(t *testing.T) {
		ran := fbSeams(t, map[string]string{"firebase.json": "{nope"}, nil, nil)
		wantRefused(t, runGroup(t, "cloud", "firebase", "deploy"), ran, 0, "not valid JSON")
	})
	t.Run("a framework source refuses", func(t *testing.T) {
		ran := fbSeams(t, map[string]string{"firebase.json": `{"hosting":{"source":"."}}`}, nil, nil)
		wantRefused(t, runGroup(t, "cloud", "firebase", "deploy"), ran, 0, "framework")
	})
	t.Run("other commands pass through", func(t *testing.T) {
		ran := fbSeams(t, nil, nil, nil)
		if err := runGroup(t, "cloud", "firebase", "login:list"); err != nil || len(*ran) != 1 {
			t.Fatalf("err %v ran %v", err, *ran)
		}
	})
}

func gcloudSeams(t *testing.T, gitErrs map[string]error, answers map[string]string) *[]string {
	t.Helper()
	withCloudSeams(t, fakeCloudFS{exists: map[string]bool{"/work/app": true, "/work/app/keys": true, "/work": true}}, nil)
	return withGuardSeams(t, &fakeGuardRunner{errs: gitErrs, answers: answers}, 0)
}

func TestCloudGcloudKeyFileInsideRepoIsRefused(t *testing.T) {
	ran := gcloudSeams(t, nil, map[string]string{"rev-parse --is-inside-work-tree": "true\n"})
	err := runGroup(t, "cloud", "gcloud", "iam", "service-accounts", "keys", "create", "key.json", "--iam-account", "sa@p.iam.gserviceaccount.com")
	wantRefused(t, err, ran, 0, "key.json", "inside a git working tree")
}

func TestCloudGcloudKeyCommandVariantsAreRefusedInsideRepo(t *testing.T) {
	cases := [][]string{
		{"beta", "iam", "service-accounts", "keys", "create", "keys/k.json", "--iam-account=sa@p"},
		{"--project", "p", "iam", "service-accounts", "keys", "create", "k.json", "--iam-account", "sa@p"},
		{"secrets", "versions", "access", "latest", "--secret", "s", "--out-file", "s.txt"},
		{"kms", "decrypt", "--plaintext-file=p.txt", "--ciphertext-file", "c"},
	}
	for _, c := range cases {
		ran := gcloudSeams(t, nil, map[string]string{"rev-parse --is-inside-work-tree": "true\n"})
		wantRefused(t, runGroup(t, append([]string{"cloud", "gcloud"}, c...)...), ran, 0, "inside a git working tree")
	}
}

func TestCloudGcloudKeyFileOutsideRepoPasses(t *testing.T) {
	ran := gcloudSeams(t, map[string]error{"rev-parse": codedErr{128}}, nil)
	args := []string{"iam", "service-accounts", "keys", "create", "/home/u/.config/k.json", "--iam-account", "sa@p"}
	if err := runGroup(t, append([]string{"cloud", "gcloud"}, args...)...); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 1 || (*ran)[0] != "gcloud "+strings.Join(args, " ") {
		t.Fatalf("ran %v", *ran)
	}
}

func TestCloudGcloudFailsClosedAndPassesTheRest(t *testing.T) {
	t.Run("git cannot answer", func(t *testing.T) {
		ran := gcloudSeams(t, map[string]error{"rev-parse": errors.New("git exploded")}, nil)
		wantRefused(t, runGroup(t, "cloud", "gcloud", "iam", "service-accounts", "keys", "create", "k.json"), ran, 0, "could not say")
	})
	t.Run("key create with no file argument", func(t *testing.T) {
		ran := gcloudSeams(t, nil, nil)
		wantRefused(t, runGroup(t, "cloud", "gcloud", "iam", "service-accounts", "keys", "create"), ran, 0, "no output file")
	})
	t.Run("key create that cannot be read", func(t *testing.T) {
		ran := gcloudSeams(t, nil, nil)
		wantRefused(t, runGroup(t, "cloud", "gcloud", "--weird", "iam", "keys", "service-accounts", "create"), ran, 0, "cannot be read")
	})
	t.Run("stdout is not a file", func(t *testing.T) {
		ran := gcloudSeams(t, nil, nil)
		if err := runGroup(t, "cloud", "gcloud", "iam", "service-accounts", "keys", "create", "-", "--iam-account", "sa@p"); err != nil || len(*ran) != 1 {
			t.Fatalf("err %v ran %v", err, *ran)
		}
	})
	t.Run("other commands pass through", func(t *testing.T) {
		ran := gcloudSeams(t, nil, nil)
		if err := runGroup(t, "cloud", "gcloud", "run", "deploy", "svc", "--project", "p"); err != nil || len(*ran) != 1 {
			t.Fatalf("err %v ran %v", err, *ran)
		}
	})
}

func TestCloudGcloudUnknownFlagCannotHideTheCommand(t *testing.T) {
	inRepo := map[string]string{"rev-parse --is-inside-work-tree": "true\n"}
	cases := [][]string{
		{"--access-token-file", "t", "secrets", "versions", "access", "latest", "--secret", "s", "--out-file", "./k"},
		{"--foo-bar", "x", "iam", "service-accounts", "keys", "create", "k.json"},
		{"iam", "service-accounts", "keys", "create", "--foo-bar", "x", "k.json"},
	}
	for _, c := range cases {
		ran := gcloudSeams(t, nil, inRepo)
		wantRefused(t, runGroup(t, append([]string{"cloud", "gcloud"}, c...)...), ran, 0, "inside a git working tree")
	}
}

func TestCloudWranglerPagesPublishAndAssetsAreScanned(t *testing.T) {
	key := "var k = \"" + cliFakeKey + "\";\n"
	t.Run("pages publish is pages deploy", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"dist": {"a.js"}}}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"dist/a.js": key}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "pages", "publish", "dist"), ran, 0, "dist/a.js:1")
	})
	t.Run("--assets directory", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"assets": {"a.js"}, "/out": {"index.js"}}}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"assets/a.js": key, "/out/index.js": "x\n"}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy", "--assets", "assets"), ran, 0, "assets/a.js:1")
	})
	t.Run("config assets directory", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"public": {"a.js"}, "/out": {"index.js"}}}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{
			"wrangler.toml": "name = \"w\"\n[assets]\ndirectory = \"./public\"\n",
			"public/a.js":   key, "/out/index.js": "x\n"}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy"), ran, 0, "public/a.js:1")
	})
	t.Run("assets in config with no readable directory refuses", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"wrangler.toml": "name = \"w\"\n[assets]\nbinding = \"A\"\n"}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy"), ran, 0, "assets")
	})
}

func TestCloudFirebasePredeployIsRefused(t *testing.T) {
	for _, cfg := range []string{
		`{"hosting":{"public":"dist","predeploy":["npm run build"]}}`,
		`{"functions":{"source":"functions","predeploy":"npm run build"}}`,
	} {
		ran := fbSeams(t, map[string]string{"firebase.json": cfg}, nil, nil)
		wantRefused(t, runGroup(t, "cloud", "firebase", "deploy"), ran, 0, "predeploy", "Build first")
	}
}

func TestCloudWranglerDryRunErrorIsCapped(t *testing.T) {
	log := "line1 first\nline2\nline3\nline4\nline5\nline6 last"
	withCloudSeams(t, fakeCloudFS{}, errors.New(log))
	ran := withGuardSeams(t, &fakeGuardRunner{}, 0)
	err := runGroup(t, "cloud", "wrangler", "deploy")
	wantRefused(t, err, ran, 0, "line6 last", "line3")
	if strings.Contains(err.Error(), "line2") || strings.Contains(err.Error(), "first") {
		t.Fatalf("the whole log is in the refusal: %v", err)
	}
}

func TestCloudDryRunWordIsNotASkip(t *testing.T) {
	key := "var k = \"" + cliFakeKey + "\";\n"
	t.Run("wrangler: --dry-run as a flag value", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"wrangler.toml": "[vars]\nK = \"" + cliFakeKey + "\"\n"}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy", "--name", "--dry-run"), ran, 0, "wrangler.toml:2")
	})
	t.Run("wrangler: --dry-run=false", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"wrangler.toml": "[vars]\nK = \"" + cliFakeKey + "\"\n"}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy", "--dry-run=false"), ran, 0, "wrangler.toml:2")
	})
	t.Run("wrangler: a user --dry-run is not passed to the bundle run twice", func(t *testing.T) {
		calls := withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"/out": {"index.js"}}}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"/out/index.js": key}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy", "--dry-run=false"), ran, 0, "/out/index.js:1")
		if len(*calls) != 1 || (*calls)[0] != "wrangler deploy --dry-run --outdir /out" {
			t.Fatalf("dry run: %v", *calls)
		}
	})
	t.Run("pages: --dry-run as a flag value", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"dist": {"a.js"}}}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"dist/a.js": key}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "pages", "deploy", "dist", "--commit-message", "--dry-run"), ran, 0, "dist/a.js:1")
	})
	t.Run("firebase: --dry-run as a flag value", func(t *testing.T) {
		ran := fbSeams(t, map[string]string{"dist/app.js": key}, nil, []string{"app.js"})
		wantRefused(t, runGroup(t, "cloud", "firebase", "deploy", "--message", "--dry-run"), ran, 0, "dist/app.js:1")
	})
}

func TestCloudGcloudFlagsFileOnKeyCommandIsRefused(t *testing.T) {
	cases := [][]string{
		{"secrets", "versions", "access", "latest", "--secret", "s", "--flags-file", "f.yaml"},
		{"secrets", "versions", "access", "latest", "--flags-file=f.yaml"},
		{"iam", "service-accounts", "keys", "create", "-", "--flags-file", "f.yaml"},
	}
	for _, c := range cases {
		ran := gcloudSeams(t, nil, nil)
		wantRefused(t, runGroup(t, append([]string{"cloud", "gcloud"}, c...)...), ran, 0, "--flags-file")
	}
	ran := gcloudSeams(t, nil, nil)
	if err := runGroup(t, "cloud", "gcloud", "run", "deploy", "--flags-file", "f.yaml"); err != nil || len(*ran) != 1 {
		t.Fatalf("a non-key command with --flags-file must pass: %v %v", err, *ran)
	}
}

func TestCloudWranglerSiteAndParentConfig(t *testing.T) {
	key := "var k = \"" + cliFakeKey + "\";\n"
	t.Run("--site directory", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"site": {"a.js"}, "/out": {"index.js"}}}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"site/a.js": key, "/out/index.js": "x\n"}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy", "--site", "site"), ran, 0, "site/a.js:1")
	})
	t.Run("config site bucket", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"public": {"a.js"}, "/out": {"index.js"}}}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{
			"wrangler.toml": "name = \"w\"\n[site]\nbucket = \"./public\"\n", "public/a.js": key, "/out/index.js": "x\n"}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy"), ran, 0, "public/a.js:1")
	})
	t.Run("site with no readable bucket refuses", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"wrangler.toml": "[site]\nentry-point = \"x\"\n"}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy"), ran, 0, "site")
	})
	t.Run("a config in a parent directory refuses", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"/work/wrangler.toml": "name = \"w\"\n"}}, 0)
		wantRefused(t, runGroup(t, "cloud", "wrangler", "deploy"), ran, 0, "/work/wrangler.toml", "Run baryovm from that directory")
	})
	t.Run("the search stops at the git root", func(t *testing.T) {
		withCloudSeams(t, fakeCloudFS{lists: map[string][]string{"/out": {"index.js"}}, exists: map[string]bool{"/work/app/.git": true}}, nil)
		ran := withGuardSeams(t, &fakeGuardRunner{files: map[string]string{"/work/wrangler.toml": "name = \"w\"\n", "/out/index.js": "x\n"}}, 0)
		if err := runGroup(t, "cloud", "wrangler", "deploy"); err != nil || len(*ran) != 1 {
			t.Fatalf("err %v ran %v", err, *ran)
		}
	})
}
