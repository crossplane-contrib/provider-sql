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
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reference"
	"github.com/pkg/errors"
)

const (
	errNoPrivileges           = "privileges not passed"
	errUnknownGrant           = "cannot identify grant type based on passed params"
	errMemberOfWithPrivileges = "cannot set privileges in the same grant as memberOf"
)

// A GrantSpec defines the desired state of a Grant.
type GrantSpec struct {
	xpv1.ResourceSpec `json:",inline"`
	ForProvider       GrantParameters `json:"forProvider"`
}

// GrantPrivilege represents a privilege to be granted
// +kubebuilder:validation:Pattern:=^[A-Z]+$
type GrantPrivilege string

// If Privileges are specified, we should have at least one

// GrantPrivileges is a list of the privileges to be granted
// +kubebuilder:validation:MinItems:=1
type GrantPrivileges []GrantPrivilege

type GrantType string

// GrantType is the list of the possible grant types represented by a GrantParameters
const (
	RoleMember             GrantType = "ROLE_MEMBER"
	RoleDatabase           GrantType = "ROLE_DATABASE"
	RoleSchema             GrantType = "ROLE_SCHEMA"
	RoleTable              GrantType = "ROLE_TABLE"
	RoleSequence           GrantType = "ROLE_SEQUENCE"
	RoleRoutine            GrantType = "ROLE_ROUTE"
	RoleColumn             GrantType = "ROLE_COLUMN"
	RoleForeignDataWrapper GrantType = "ROLE_FOREIGN_DATA_WRAPPER"
	RoleForeignServer      GrantType = "ROLE_FOREIGN_SERVER"
)

type marker struct{}
type stringSet struct {
	elements map[string]marker
}

func newStringSet() *stringSet {
	return &stringSet{
		elements: make(map[string]marker),
	}
}

func (s *stringSet) add(element string) {
	s.elements[element] = marker{}
}

func (s *stringSet) contains(element string) bool {
	_, exists := s.elements[element]
	return exists
}

func (s *stringSet) containsExactly(elements ...string) bool {
	if len(s.elements) != len(elements) {
		return false
	}
	for _, elem := range elements {
		if !s.contains(elem) {
			return false
		}
	}
	return true
}

func (gp *GrantParameters) filledInFields() *stringSet {
	fields := map[string]bool{
		"MemberOf":            gp.MemberOf != nil,
		"Database":            gp.Database != nil,
		"Schema":              gp.Schema != nil,
		"Tables":              len(gp.Tables) > 0,
		"Columns":             len(gp.Columns) > 0,
		"Sequences":           len(gp.Sequences) > 0,
		"Routines":            len(gp.Routines) > 0,
		"ForeignServers":      len(gp.ForeignServers) > 0,
		"ForeignDataWrappers": len(gp.ForeignDataWrappers) > 0,
	}
	set := newStringSet()

	for key, hasField := range fields {
		if hasField {
			set.add(key)
		}
	}
	return set
}

var grantTypeFields = map[GrantType][]string{
	RoleMember:             {"MemberOf"},
	RoleDatabase:           {"Database"},
	RoleSchema:             {"Database", "Schema"},
	RoleTable:              {"Database", "Schema", "Tables"},
	RoleColumn:             {"Database", "Schema", "Tables", "Columns"},
	RoleSequence:           {"Database", "Schema", "Sequences"},
	RoleRoutine:            {"Database", "Schema", "Routines"},
	RoleForeignServer:      {"Database", "ForeignServers"},
	RoleForeignDataWrapper: {"Database", "ForeignDataWrappers"},
}

// IdentifyGrantType return the deduced GrantType from the filled in fields.
func (gp *GrantParameters) IdentifyGrantType() (GrantType, error) {
	ff := gp.filledInFields()
	pc := len(gp.Privileges)

	var gt *GrantType

	for k, v := range grantTypeFields {
		if ff.containsExactly(v...) {
			gt = &k
			break
		}
	}
	if gt == nil {
		return "", errors.New(errUnknownGrant)
	}
	if *gt == RoleMember && pc > 0 {
		return "", errors.New(errMemberOfWithPrivileges)
	}
	if *gt != RoleMember && pc < 1 {
		return "", errors.New(errNoPrivileges)
	}
	return *gt, nil
}

// Some privileges are shorthands for multiple privileges. These translations
// happen internally inside postgresql when making grants. When we query the
// privileges back, we need to look for the expanded set.
// https://www.postgresql.org/docs/15/ddl-priv.html
var grantReplacements = map[GrantType]map[GrantPrivilege]GrantPrivileges{
	RoleDatabase: {
		"ALL":            {"CREATE", "TEMPORARY", "CONNECT"},
		"ALL PRIVILEGES": {"CREATE", "TEMPORARY", "CONNECT"},
		"TEMP":           {"TEMPORARY"},
	},
	RoleSchema: {
		"ALL":            {"CREATE", "USAGE"},
		"ALL PRIVILEGES": {"CREATE", "USAGE"},
	},
	RoleTable: {
		"ALL":            {"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "MAINTAIN"},
		"ALL PRIVILEGES": {"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER", "MAINTAIN"},
	},
	RoleColumn: {
		"ALL":            {"SELECT", "INSERT", "UPDATE", "REFERENCES"},
		"ALL PRIVILEGES": {"SELECT", "INSERT", "UPDATE", "REFERENCES"},
	},
	RoleSequence: {
		"ALL":            {"USAGE", "SELECT", "UPDATE"},
		"ALL PRIVILEGES": {"USAGE", "SELECT", "UPDATE"},
	},
	RoleRoutine: {
		"ALL":            {"EXECUTE"},
		"ALL PRIVILEGES": {"EXECUTE"},
	},
	RoleForeignDataWrapper: {
		"ALL":            {"USAGE"},
		"ALL PRIVILEGES": {"USAGE"},
	},
	RoleForeignServer: {
		"ALL":            {"USAGE"},
		"ALL PRIVILEGES": {"USAGE"},
	},
}

// ExpandPrivileges expands any shorthand privileges to their full equivalents.
func (gp *GrantParameters) ExpandPrivileges() GrantPrivileges {
	gt, err := gp.IdentifyGrantType()
	if err != nil {
		return gp.Privileges
	}
	gr, ex := grantReplacements[gt]
	if !ex {
		return gp.Privileges
	}

	privilegeSet := make(map[GrantPrivilege]struct{})

	// Replace any shorthand privileges with their full equivalents
	for _, p := range gp.Privileges {
		if _, ok := gr[p]; ok {
			for _, rp := range gr[p] {
				privilegeSet[rp] = struct{}{}
			}
		} else {
			privilegeSet[p] = struct{}{}
		}
	}

	privileges := make([]GrantPrivilege, 0, len(privilegeSet))
	for p := range privilegeSet {
		privileges = append(privileges, p)
	}

	return privileges
}

// ToStringSlice converts the slice of privileges to strings
func (gp *GrantPrivileges) ToStringSlice() []string {
	if gp == nil {
		return []string{}
	}
	out := make([]string, len(*gp))
	for i, v := range *gp {
		out[i] = string(v)
	}
	return out
}

// GrantOption represents an OPTION that will be applied to a grant.
// This modifies the behaviour of the grant depending on the type of
// grant and option applied.
type GrantOption string

// The possible values for grant option type.
const (
	GrantOptionAdmin GrantOption = "ADMIN"
	GrantOptionGrant GrantOption = "GRANT"
)

type Routine struct {
	// The name of the routine.
	Name string `json:"name,omitempty"`

	// The arguments of the routine.
	// +optional
	Arguments []string `json:"args,omitempty"`
}

// GrantParameters define the desired state of a PostgreSQL grant instance.
type GrantParameters struct {
	// Privileges to be granted.
	// See https://www.postgresql.org/docs/current/sql-grant.html for available privileges.
	// +optional
	Privileges GrantPrivileges `json:"privileges,omitempty"`

	// WithOption allows an option to be set on the grant.
	// See https://www.postgresql.org/docs/current/sql-grant.html for available
	// options for each grant type, and the effects of applying the option.
	// +kubebuilder:validation:Enum=ADMIN;GRANT
	// +optional
	WithOption *GrantOption `json:"withOption,omitempty"`

	// Role this grant is for.
	// +optional
	Role *string `json:"role,omitempty"`

	// RoleRef references the role object this grant is for.
	// +immutable
	// +optional
	RoleRef *xpv1.Reference `json:"roleRef,omitempty"`

	// RoleSelector selects a reference to a Role this grant is for.
	// +immutable
	// +optional
	RoleSelector *xpv1.Selector `json:"roleSelector,omitempty"`

	// Database this grant is for.
	// +optional
	Database *string `json:"database,omitempty"`

	// DatabaseRef references the database object this grant it for.
	// +immutable
	// +optional
	DatabaseRef *xpv1.Reference `json:"databaseRef,omitempty"`

	// DatabaseSelector selects a reference to a Database this grant is for.
	// +immutable
	// +optional
	DatabaseSelector *xpv1.Selector `json:"databaseSelector,omitempty"`

	// Schema this grant is for.
	// +optional
	Schema *string `json:"schema,omitempty"`

	// SchemaRef references the schema object this grant it for.
	// +immutable
	// +optional
	SchemaRef *xpv1.Reference `json:"schemaRef,omitempty"`

	// SchemaSelector selects a reference to a Schema this grant is for.
	// +immutable
	// +optional
	SchemaSelector *xpv1.Selector `json:"schemaSelector,omitempty"`

	// MemberOf is the Role that this grant makes Role a member of.
	// +optional
	MemberOf *string `json:"memberOf,omitempty"`

	// MemberOfRef references the Role that this grant makes Role a member of.
	// +immutable
	// +optional
	MemberOfRef *xpv1.Reference `json:"memberOfRef,omitempty"`

	// MemberOfSelector selects a reference to a Role that this grant makes Role a member of.
	// +immutable
	// +optional
	MemberOfSelector *xpv1.Selector `json:"memberOfSelector,omitempty"`

	// RevokePublicOnDb apply the statement "REVOKE ALL ON DATABASE %s FROM PUBLIC" to make database unreachable from public
	// +optional
	RevokePublicOnDb *bool `json:"revokePublicOnDb,omitempty" default:"false"`

	// The columns upon which to grant the privileges.
	// +optional
	Columns []string `json:"columns,omitempty"`

	// The tables upon which to grant the privileges.
	// +optional
	Tables []string `json:"tables,omitempty"`

	// The sequences upon which to grant the privileges.
	// +optional
	Sequences []string `json:"sequences,omitempty"`

	// The routines upon which to grant the privileges.
	// +optional
	Routines []Routine `json:"routines,omitempty"`

	// The foreign data wrappers upon which to grant the privileges.
	// +optional
	ForeignDataWrappers []string `json:"foreignDataWrappers,omitempty"`

	// The foreign servers upon which to grant the privileges.
	// +optional
	ForeignServers []string `json:"foreignServers,omitempty"`
}

// A GrantStatus represents the observed state of a Grant.
type GrantStatus struct {
	xpv1.ResourceStatus `json:",inline"`
}

// +kubebuilder:object:root=true

// A Grant represents the declarative state of a PostgreSQL grant.
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:printcolumn:name="ROLE",type="string",JSONPath=".spec.forProvider.role"
// +kubebuilder:printcolumn:name="MEMBER OF",type="string",JSONPath=".spec.forProvider.memberOf"
// +kubebuilder:printcolumn:name="DATABASE",type="string",JSONPath=".spec.forProvider.database"
// +kubebuilder:printcolumn:name="PRIVILEGES",type="string",JSONPath=".spec.forProvider.privileges"
// +kubebuilder:resource:scope=Cluster,categories={crossplane,managed,sql}
type Grant struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GrantSpec   `json:"spec"`
	Status GrantStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GrantList contains a list of Grant
type GrantList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Grant `json:"items"`
}

// ResolveReferences of this Grant
func (mg *Grant) ResolveReferences(ctx context.Context, c client.Reader) error {
	r := reference.NewAPIResolver(c, mg)

	// Resolve spec.forProvider.database
	rsp, err := r.Resolve(ctx, reference.ResolutionRequest{
		CurrentValue: reference.FromPtrValue(mg.Spec.ForProvider.Database),
		Reference:    mg.Spec.ForProvider.DatabaseRef,
		Selector:     mg.Spec.ForProvider.DatabaseSelector,
		To:           reference.To{Managed: &Database{}, List: &DatabaseList{}},
		Extract:      reference.ExternalName(),
	})
	if err != nil {
		return errors.Wrap(err, "spec.forProvider.database")
	}
	mg.Spec.ForProvider.Database = reference.ToPtrValue(rsp.ResolvedValue)
	mg.Spec.ForProvider.DatabaseRef = rsp.ResolvedReference

	// Resolve spec.forProvider.schema
	rsp, err = r.Resolve(ctx, reference.ResolutionRequest{
		CurrentValue: reference.FromPtrValue(mg.Spec.ForProvider.Schema),
		Reference:    mg.Spec.ForProvider.SchemaRef,
		Selector:     mg.Spec.ForProvider.SchemaSelector,
		To:           reference.To{Managed: &Schema{}, List: &SchemaList{}},
		Extract:      reference.ExternalName(),
	})
	if err != nil {
		return errors.Wrap(err, "spec.forProvider.schema")
	}
	mg.Spec.ForProvider.Schema = reference.ToPtrValue(rsp.ResolvedValue)
	mg.Spec.ForProvider.SchemaRef = rsp.ResolvedReference

	// Resolve spec.forProvider.role
	rsp, err = r.Resolve(ctx, reference.ResolutionRequest{
		CurrentValue: reference.FromPtrValue(mg.Spec.ForProvider.Role),
		Reference:    mg.Spec.ForProvider.RoleRef,
		Selector:     mg.Spec.ForProvider.RoleSelector,
		To:           reference.To{Managed: &Role{}, List: &RoleList{}},
		Extract:      reference.ExternalName(),
	})
	if err != nil {
		return errors.Wrap(err, "spec.forProvider.role")
	}
	mg.Spec.ForProvider.Role = reference.ToPtrValue(rsp.ResolvedValue)
	mg.Spec.ForProvider.RoleRef = rsp.ResolvedReference

	// Resolve spec.forProvider.memberOf
	rsp, err = r.Resolve(ctx, reference.ResolutionRequest{
		CurrentValue: reference.FromPtrValue(mg.Spec.ForProvider.MemberOf),
		Reference:    mg.Spec.ForProvider.MemberOfRef,
		Selector:     mg.Spec.ForProvider.MemberOfSelector,
		To:           reference.To{Managed: &Role{}, List: &RoleList{}},
		Extract:      reference.ExternalName(),
	})
	if err != nil {
		return errors.Wrap(err, "spec.forProvider.memberOf")
	}
	mg.Spec.ForProvider.MemberOf = reference.ToPtrValue(rsp.ResolvedValue)
	mg.Spec.ForProvider.MemberOfRef = rsp.ResolvedReference
	return nil
}
