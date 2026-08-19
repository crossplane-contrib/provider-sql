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
	"regexp"
	"testing"
)

// routineArgumentPattern must match the +kubebuilder:validation:items:Pattern
// marker on Routine.Arguments. It's the only thing standing between that
// field and arbitrary SQL, since the reconciler splices each argument
// unquoted into a GRANT/REVOKE statement -- see quotedSignatures in
// pkg/controller/namespaced/postgresql/grant/reconciler.go. Keep this in
// sync with the marker by hand; there is no way to read kubebuilder markers
// back out of the compiled type at runtime.
const routineArgumentPattern = `^[a-zA-Z_][a-zA-Z0-9_$]*(\.[a-zA-Z_][a-zA-Z0-9_$]*)?$`

func TestRoutineArgumentPattern(t *testing.T) {
	re := regexp.MustCompile(routineArgumentPattern)

	cases := map[string]struct {
		arg     string
		matches bool
	}{
		"PlainIdentifier": {
			arg:     "text",
			matches: true,
		},
		"SchemaQualifiedCompositeType": {
			arg:     "aws_commons._s3_uri_1",
			matches: true,
		},
		"SchemaQualifiedWithUnderscoresAndDollar": {
			arg:     "my_schema$1.my_type$2",
			matches: true,
		},
		"EmptyString": {
			arg:     "",
			matches: false,
		},
		"LeadingDigit": {
			arg:     "1text",
			matches: false,
		},
		"TrailingDot": {
			arg:     "aws_commons.",
			matches: false,
		},
		"LeadingDot": {
			arg:     ".aws_commons",
			matches: false,
		},
		"TwoDots": {
			arg:     "a.b.c",
			matches: false,
		},
		"EmbeddedSpace": {
			arg:     "foo bar",
			matches: false,
		},
		"SpaceAroundDot": {
			arg:     "foo. bar",
			matches: false,
		},
		"SQLInjectionSemicolon": {
			arg:     "text; DROP TABLE users",
			matches: false,
		},
		"SQLInjectionQuote": {
			arg:     `text" OR "1"="1`,
			matches: false,
		},
		"SQLInjectionParens": {
			arg:     "text)--",
			matches: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := re.MatchString(tc.arg)
			if got != tc.matches {
				t.Errorf("routineArgumentPattern.MatchString(%q) = %v, want %v", tc.arg, got, tc.matches)
			}
		})
	}
}
