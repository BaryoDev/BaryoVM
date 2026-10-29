## baryovm stack set-managed

Name the managed database beside a stack and the AWS profile or Azure subscription that reaches it

### Synopsis

Stores which managed database a stack uses and which AWS profile (with a region) or Azure
subscription (with a resource group) reaches it. Only names are stored. BaryoVM runs the
`aws` or `az` already installed and signed in on this machine; no key, token or password
is read or written.

A release then snapshots the database first and waits for it (`aws rds create-db-snapshot`
and `aws rds wait db-snapshot-available`, or `az postgres flexible-server backup create`).
--no-backup skips it. `baryovm doctor` checks the CLI is installed and signed in.

```
baryovm stack set-managed <name> [flags]
```

### Examples

```
  baryovm stack set-managed shop --provider aws --profile prod --region ap-southeast-1 --database shop-db
  baryovm stack set-managed shop --provider azure --subscription prod-sub --resource-group shop-rg --database shop-pg
  baryovm stack set-managed shop --clear
```

### Options

```
      --clear                   remove the stack's managed database
      --database string         RDS instance identifier or flexible server name
  -h, --help                    help for set-managed
      --profile string          AWS CLI profile name (aws; empty uses the default profile)
      --provider string         aws or azure
      --region string           AWS region (aws)
      --resource-group string   Azure resource group (azure)
      --subscription string     Azure subscription id or name (azure)
```

### Options inherited from parent commands

```
  -o, --output string      output format: human | json (default "human")
      --strict-host-keys   refuse an unknown host instead of learning its key (also BARYOVM_STRICT_HOST_KEYS=1)
```

### SEE ALSO

* [baryovm stack](baryovm_stack.md)	 - Manage docker compose stacks on your VMs
