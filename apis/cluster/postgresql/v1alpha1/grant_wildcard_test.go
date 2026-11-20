/*
Copyright 2020 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"strings"
	"testing"

	"k8s.io/utils/ptr"
)

const (
	privSelect = "SELECT"
	objMyTable = "mytable"
)

func TestIsWildcard(t *testing.T) {
	cases := map[string]struct {
		objs []string
		want bool
	}{
		"Wildcard":          {[]string{"*"}, true},
		"Nil":               {nil, false},
		"Empty":             {[]string{}, false},
		"SingleName":        {[]string{objMyTable}, false},
		"WildcardThenName":  {[]string{"*", objMyTable}, false},
		"NameThenWildcard":  {[]string{objMyTable, "*"}, false},
		"RepeatedWildcards": {[]string{"*", "*"}, false},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := IsWildcard(tc.objs); got != tc.want {
				t.Errorf("IsWildcard(%v) = %v, want %v", tc.objs, got, tc.want)
			}
		})
	}
}

func TestIsWildcardRoutines(t *testing.T) {
	cases := map[string]struct {
		rs   []Routine
		want bool
	}{
		"Wildcard":         {[]Routine{{Name: "*"}}, true},
		"Nil":              {nil, false},
		"SingleName":       {[]Routine{{Name: "myfunc"}}, false},
		"WildcardThenName": {[]Routine{{Name: "*"}, {Name: "myfunc"}}, false},
		// Args are rejected separately by validateWildcards.
		"WildcardWithArgs": {[]Routine{{Name: "*", Arguments: []string{"text"}}}, true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := IsWildcardRoutines(tc.rs); got != tc.want {
				t.Errorf("IsWildcardRoutines(%v) = %v, want %v", tc.rs, got, tc.want)
			}
		})
	}
}

// TestIdentifyGrantTypeWildcard pins that the sentinel rides the existing
// field-presence dispatch untouched: `tables: ["*"]` has length 1, so "Tables"
// is still a filled-in field and the grant still resolves to RoleTable.
func TestIdentifyGrantTypeWildcard(t *testing.T) {
	cases := map[string]struct {
		reason  string
		gp      GrantParameters
		want    GrantType
		wantErr string
	}{
		"TablesWildcardIsATableGrant": {
			reason: "A wildcard table list must dispatch to RoleTable, not fall through to RoleSchema",
			gp: GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Role:       ptr.To("myrole"),
				Privileges: GrantPrivileges{privSelect},
				Tables:     []string{"*"},
			},
			want: RoleTable,
		},
		"SequencesWildcardIsASequenceGrant": {
			reason: "A wildcard sequence list must dispatch to RoleSequence",
			gp: GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Role:       ptr.To("myrole"),
				Privileges: GrantPrivileges{privSelect},
				Sequences:  []string{"*"},
			},
			want: RoleSequence,
		},
		"RoutinesWildcardIsARoutineGrant": {
			reason: "A wildcard routine list must dispatch to RoleRoutine",
			gp: GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Role:       ptr.To("myrole"),
				Privileges: GrantPrivileges{"EXECUTE"},
				Routines:   []Routine{{Name: "*"}},
			},
			want: RoleRoutine,
		},
		"MixedTableWildcardRejected": {
			reason: "ON ALL TABLES IN SCHEMA cannot be combined with named tables, so mixing must fail loudly rather than silently granting on everything",
			gp: GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Role:       ptr.To("myrole"),
				Privileges: GrantPrivileges{privSelect},
				Tables:     []string{"*", objMyTable},
			},
			wantErr: "must be the only element",
		},
		"MixedSequenceWildcardRejected": {
			reason: "Same reasoning as tables",
			gp: GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Role:       ptr.To("myrole"),
				Privileges: GrantPrivileges{"USAGE"},
				Sequences:  []string{"myseq", "*"},
			},
			wantErr: "must be the only element",
		},
		"MixedRoutineWildcardRejected": {
			reason: "Same reasoning as tables",
			gp: GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Role:       ptr.To("myrole"),
				Privileges: GrantPrivileges{"EXECUTE"},
				Routines:   []Routine{{Name: "*"}, {Name: "myfunc"}},
			},
			wantErr: "must be the only element",
		},
		"RoutineWildcardWithArgsRejected": {
			reason: "ON ALL ROUTINES IN SCHEMA takes no signature, so a wildcard carrying args is a contradiction",
			gp: GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Role:       ptr.To("myrole"),
				Privileges: GrantPrivileges{"EXECUTE"},
				Routines:   []Routine{{Name: "*", Arguments: []string{"text"}}},
			},
			wantErr: "must not declare args",
		},
		"ColumnWildcardRejected": {
			reason: "PostgreSQL has no column-level ON ALL form: GRANT SELECT (col) ON ALL TABLES IN SCHEMA is not valid SQL",
			gp: GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Role:       ptr.To("myrole"),
				Privileges: GrantPrivileges{privSelect},
				Tables:     []string{objMyTable},
				Columns:    []string{"*"},
			},
			wantErr: "not supported",
		},
		"TableWildcardInColumnGrantRejected": {
			reason: "Wildcarding the tables of a column grant hits the same missing SQL form",
			gp: GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Role:       ptr.To("myrole"),
				Privileges: GrantPrivileges{privSelect},
				Tables:     []string{"*"},
				Columns:    []string{"mycol"},
			},
			wantErr: "not supported",
		},
		"ForeignServerWildcardRejected": {
			reason: "PostgreSQL has no GRANT ... ON ALL FOREIGN SERVERS",
			gp: GrantParameters{
				Database:       ptr.To("mydb"),
				Role:           ptr.To("myrole"),
				Privileges:     GrantPrivileges{"USAGE"},
				ForeignServers: []string{"*"},
			},
			wantErr: "not supported",
		},
		"ForeignDataWrapperWildcardRejected": {
			reason: "PostgreSQL has no GRANT ... ON ALL FOREIGN DATA WRAPPERS",
			gp: GrantParameters{
				Database:            ptr.To("mydb"),
				Role:                ptr.To("myrole"),
				Privileges:          GrantPrivileges{"USAGE"},
				ForeignDataWrappers: []string{"*"},
			},
			wantErr: "not supported",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := tc.gp.IdentifyGrantType()

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("%s\nwant error containing %q, got nil (type %s)", tc.reason, tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("%s\nwant error containing %q, got %q", tc.reason, tc.wantErr, err.Error())
				}
				return
			}

			if err != nil {
				t.Fatalf("%s\nunexpected error: %v", tc.reason, err)
			}
			if got != tc.want {
				t.Errorf("%s\nIdentifyGrantType() = %s, want %s", tc.reason, got, tc.want)
			}
		})
	}
}
