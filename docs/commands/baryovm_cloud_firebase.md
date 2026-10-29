## baryovm cloud firebase

Run firebase after scanning the files it deploys and checking the project

### Synopsis

Runs firebase with the same arguments. For deploy and hosting:channel:deploy it
first checks two things:

  project  the project id is read from --project (an id or an alias from
           .firebaserc), then the default in .firebaserc, printed, and checked
           against the allowed cloud projects; one that is not on the list is
           refused before firebase runs. If the id cannot be determined the deploy
           is refused. While a project list is set, --project is required, because
           the active project that "firebase use" keeps outside the repository
           cannot be read here
  files    every file in the hosting public directories and functions sources
           named in firebase.json is scanned, except files matched by that entry's
           ignore globs. A framework source is refused: what it builds cannot be listed.
           An entry with predeploy is refused: build first, then remove predeploy

A key is refused with the file and line, never the value. Other firebase commands
pass through. Put options after the command: a leading option is refused.

The allowed projects come from git config (one --add per project id) and the
BARYOVM_ALLOWED_CLOUD_PROJECTS environment variable (comma separated). With
neither set, any project is allowed:

  git config --add baryovm.allowedCloudProjects my-project-id

Flags after "firebase" belong to firebase, so this command takes none of its own.
For JSON output of a refusal set BARYOVM_OUTPUT=json.

```
baryovm cloud firebase <firebase arguments> [flags]
```

### Examples

```
  baryovm cloud firebase deploy --only hosting --project my-project-id
```

### Options

```
  -h, --help   help for firebase
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm cloud](baryovm_cloud.md)	 - Run wrangler, firebase or gcloud, refusing a deploy that ships a key or aims at the wrong project
