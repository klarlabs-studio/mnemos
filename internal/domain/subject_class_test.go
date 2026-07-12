package domain

import "testing"

func TestSubjectClass_Normalized(t *testing.T) {
	cases := map[string]SubjectClass{
		"":            SubjectClassUnknown,
		"individual":  SubjectClassIndividual,
		" Individual": SubjectClassIndividual,
		"CLASS":       SubjectClassClass,
		"class ":      SubjectClassClass,
		"nonsense":    SubjectClassUnknown,
	}
	for in, want := range cases {
		if got := SubjectClass(in).Normalized(); got != want {
			t.Errorf("SubjectClass(%q).Normalized() = %q, want %q", in, got, want)
		}
	}
}

func TestEntityType_IsClassLevel(t *testing.T) {
	classLevel := []EntityType{EntityTypeConcept}
	individual := []EntityType{EntityTypePerson, EntityTypeOrg, EntityTypeProject, EntityTypeProduct, EntityTypePlace}
	for _, et := range classLevel {
		if !et.IsClassLevel() {
			t.Errorf("EntityType %q should be class-level", et)
		}
	}
	for _, et := range individual {
		if et.IsClassLevel() {
			t.Errorf("EntityType %q should NOT be class-level", et)
		}
	}
}

func TestSubjectClassFromEntityTypes(t *testing.T) {
	cases := []struct {
		name  string
		types []EntityType
		want  SubjectClass
	}{
		{"no subjects", nil, SubjectClassUnknown},
		{"only concept", []EntityType{EntityTypeConcept}, SubjectClassClass},
		{"two concepts", []EntityType{EntityTypeConcept, EntityTypeConcept}, SubjectClassClass},
		{"person taints", []EntityType{EntityTypeConcept, EntityTypePerson}, SubjectClassIndividual},
		{"only person", []EntityType{EntityTypePerson}, SubjectClassIndividual},
		{"org individual", []EntityType{EntityTypeOrg}, SubjectClassIndividual},
	}
	for _, tc := range cases {
		if got := SubjectClassFromEntityTypes(tc.types); got != tc.want {
			t.Errorf("%s: SubjectClassFromEntityTypes(%v) = %q, want %q", tc.name, tc.types, got, tc.want)
		}
	}
}

func TestAggregateSubjectClass(t *testing.T) {
	cases := []struct {
		name    string
		classes []SubjectClass
		want    SubjectClass
	}{
		{"empty", nil, SubjectClassUnknown},
		{"all class", []SubjectClass{SubjectClassClass, SubjectClassClass}, SubjectClassClass},
		{"any individual", []SubjectClass{SubjectClassClass, SubjectClassIndividual}, SubjectClassIndividual},
		{"unknown without individual", []SubjectClass{SubjectClassClass, SubjectClassUnknown}, SubjectClassUnknown},
		{"individual beats unknown", []SubjectClass{SubjectClassUnknown, SubjectClassIndividual}, SubjectClassIndividual},
	}
	for _, tc := range cases {
		if got := AggregateSubjectClass(tc.classes); got != tc.want {
			t.Errorf("%s: AggregateSubjectClass(%v) = %q, want %q", tc.name, tc.classes, got, tc.want)
		}
	}
}

func TestEligibleForPromotion(t *testing.T) {
	if !EligibleForPromotion(SubjectClassClass) {
		t.Error("class must be eligible for promotion")
	}
	for _, c := range []SubjectClass{SubjectClassIndividual, SubjectClassUnknown, "garbage"} {
		if EligibleForPromotion(c) {
			t.Errorf("subject class %q must NOT be eligible (fail closed)", c)
		}
	}
}
