// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package managed

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

var (
	awsSpec = Spec{Provider: AWS, Profile: "prod", Region: "ap-southeast-1", Database: "shop-db"}
	azSpec  = Spec{Provider: Azure, Subscription: "prod-sub", ResourceGroup: "shop-rg", Database: "shop-pg"}
)

func TestAWSSnapshotCallsAreCreateThenWait(t *testing.T) {
	got, err := awsSpec.SnapshotArgvs("baryovm-1")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"aws", "--profile", "prod", "--region", "ap-southeast-1", "sts", "get-caller-identity"},
		{"aws", "--profile", "prod", "--region", "ap-southeast-1", "rds", "create-db-snapshot",
			"--db-instance-identifier", "shop-db", "--db-snapshot-identifier", "baryovm-1"},
		{"aws", "--profile", "prod", "--region", "ap-southeast-1", "rds", "wait", "db-snapshot-available",
			"--db-snapshot-identifier", "baryovm-1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestAWSWithoutAProfileOmitsTheFlag(t *testing.T) {
	s := awsSpec
	s.Profile = ""
	got, err := s.SnapshotArgvs("baryovm-1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(Join(got[0]), "--profile") {
		t.Errorf("no profile named, none expected: %v", got[0])
	}
}

func TestAzureBackupCall(t *testing.T) {
	got, err := azSpec.SnapshotArgvs("baryovm-1")
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"az", "account", "show", "--subscription", "prod-sub"},
		{"az", "postgres", "flexible-server", "backup", "create", "--subscription", "prod-sub",
			"--resource-group", "shop-rg", "--name", "shop-pg", "--backup-name", "baryovm-1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v", got)
	}
}

func TestValidateRefusesBadValues(t *testing.T) {
	bad := map[string]Spec{
		"unknown provider": {Provider: "gcp", Database: "x"},
		"aws no region":    {Provider: AWS, Database: "db"},
		"aws bad region":   {Provider: AWS, Region: "mars", Database: "db"},
		"aws dash profile": {Provider: AWS, Profile: "--debug", Region: "us-east-1", Database: "db"},
		"aws space":        {Provider: AWS, Profile: "a b", Region: "us-east-1", Database: "db"},
		"aws semicolon db": {Provider: AWS, Region: "us-east-1", Database: "db;rm"},
		"aws dash db":      {Provider: AWS, Region: "us-east-1", Database: "-db"},
		"aws no db":        {Provider: AWS, Region: "us-east-1"},
		"aws azure field":  {Provider: AWS, Region: "us-east-1", Database: "db", ResourceGroup: "rg"},
		"az no group":      {Provider: Azure, Subscription: "s", Database: "d"},
		"az dash sub":      {Provider: Azure, Subscription: "-s", ResourceGroup: "rg", Database: "d"},
		"az dash group":    {Provider: Azure, Subscription: "s", ResourceGroup: "--x", Database: "d"},
		"az dash db":       {Provider: Azure, Subscription: "s", ResourceGroup: "rg", Database: "-d"},
		"az newline":       {Provider: Azure, Subscription: "s", ResourceGroup: "rg\nx", Database: "d"},
		"az aws field":     {Provider: Azure, Subscription: "s", ResourceGroup: "rg", Database: "d", Region: "us-east-1"},
	}
	for name, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%s: want a refusal", name)
		}
		if _, err := s.SnapshotArgvs("baryovm-1"); err == nil {
			t.Errorf("%s: no argv may be built from a bad spec", name)
		}
	}
	if err := awsSpec.Validate(); err != nil {
		t.Error(err)
	}
	if err := azSpec.Validate(); err != nil {
		t.Error(err)
	}
}

func TestSnapshotIDMustBeSafe(t *testing.T) {
	if _, err := awsSpec.SnapshotArgvs("-x"); err == nil {
		t.Error("an id starting with a dash must be refused")
	}
	if id := SnapshotID(time.Date(2026, 10, 30, 1, 2, 3, 0, time.UTC)); id != "baryovm-20261030-010203" {
		t.Errorf("id %q", id)
	}
}

// No value in any argv, past the program name, may start with a dash unless it is one of the flags we wrote.
func TestNoValueReadsAsAFlag(t *testing.T) {
	flags := map[string]bool{
		"--profile": true, "--region": true, "--db-instance-identifier": true, "--db-snapshot-identifier": true,
		"--subscription": true, "--resource-group": true, "--name": true, "--backup-name": true,
	}
	for _, s := range []Spec{awsSpec, azSpec} {
		argvs, _ := s.SnapshotArgvs("baryovm-1")
		for _, a := range argvs {
			for i, v := range a[1:] {
				if strings.HasPrefix(v, "-") && !flags[v] {
					t.Errorf("%v: unexpected flag-like %q", a, v)
				}
				if i > 0 && flags[a[1:][i-1]] && strings.HasPrefix(v, "-") {
					t.Errorf("%v: value %q follows a flag and starts with a dash", a, v)
				}
			}
		}
	}
}

func TestTrimKeepsTheEnd(t *testing.T) {
	long := strings.Repeat("a", MaxOutput) + "the error"
	if got := Trim(long); !strings.HasSuffix(got, "the error") || len(got) > MaxOutput+3 {
		t.Errorf("len %d", len(got))
	}
}

func TestHintsNameTheCommand(t *testing.T) {
	if h := awsSpec.SignInHint(); !strings.Contains(h, "aws sso login --profile prod") {
		t.Error(h)
	}
	if h := azSpec.SignInHint(); !strings.Contains(h, "az login") {
		t.Error(h)
	}
}
