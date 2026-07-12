package domain

import "strings"

// SubjectClass classifies WHAT a Belief or Schema is about, so the ADR 0012
// promotion pass can decide eligibility for the shared global brain BEFORE it
// counts corroborating sources. It answers "is this about a specific instance
// or about a category?":
//
//   - SubjectClassIndividual — about a specific entity (this pet, this owner).
//     PRIVATE: it must never leave its tenant, regardless of corroboration.
//   - SubjectClassClass — about a category (a breed, a species, a disease, a
//     newly-encountered spider). Eligible to become global knowledge.
//   - SubjectClassUnknown — unclassified (the empty value). Treated as NOT
//     eligible: for a medical product the safe default is to keep anything
//     unclassified private until it is positively identified as class-level.
//
// The type is a string so it round-trips cleanly through JSON/DB columns and so
// an empty value naturally means "unknown".
type SubjectClass string

// Supported SubjectClass values. The empty string is the canonical "unknown"
// value so a zero Belief/Schema is unclassified (and therefore ineligible).
const (
	// SubjectClassUnknown is the unclassified value (the empty string). It is
	// NOT eligible for promotion — fail closed.
	SubjectClassUnknown SubjectClass = ""
	// SubjectClassIndividual marks knowledge about a specific instance (a
	// particular pet/owner/customer). It is private and never promotes.
	SubjectClassIndividual SubjectClass = "individual"
	// SubjectClassClass marks knowledge about a category (breed, species,
	// disease, a spider). It is eligible for the shared global brain.
	SubjectClassClass SubjectClass = "class"
)

// Normalized folds free-form input (case, surrounding whitespace) to a
// canonical SubjectClass, mapping anything unrecognised to
// SubjectClassUnknown. This keeps an operator hint like " Class " or a stray
// value from silently widening eligibility.
func (c SubjectClass) Normalized() SubjectClass {
	switch SubjectClass(strings.ToLower(strings.TrimSpace(string(c)))) {
	case SubjectClassIndividual:
		return SubjectClassIndividual
	case SubjectClassClass:
		return SubjectClassClass
	default:
		return SubjectClassUnknown
	}
}

// IsClassLevel reports whether an EntityType names a category/abstraction
// (class-level) rather than a specific named instance (individual-level). Only
// Concept is class-level; person/org/project/product/place each name a specific
// entity and are therefore individual-level. The bias is deliberately
// conservative — for a medical product, unless a subject is positively a
// category it is treated as an individual and kept private.
func (t EntityType) IsClassLevel() bool {
	return t == EntityTypeConcept
}

// ClassifyEntitySubject maps a single entity type to the subject class of a
// belief whose only subject is that entity: a Concept is class-level, every
// other entity type is individual-level.
func ClassifyEntitySubject(t EntityType) SubjectClass {
	if t.IsClassLevel() {
		return SubjectClassClass
	}
	return SubjectClassIndividual
}

// SubjectClassFromEntityTypes infers a belief's subject class from the types of
// its SUBJECT entities (the entities the claim is about). It fails closed:
//
//   - no subject entities              → SubjectClassUnknown (ineligible)
//   - ANY individual-level subject     → SubjectClassIndividual (private)
//   - every subject is class-level     → SubjectClassClass (eligible)
//
// A single individual subject taints the whole belief as individual, because a
// statement about "Rex, a Golden Retriever" is about Rex.
func SubjectClassFromEntityTypes(types []EntityType) SubjectClass {
	if len(types) == 0 {
		return SubjectClassUnknown
	}
	for _, t := range types {
		if !t.IsClassLevel() {
			return SubjectClassIndividual
		}
	}
	return SubjectClassClass
}

// AggregateSubjectClass combines the subject classes of the beliefs backing a
// Schema into the schema's own class — "a lesson over class-level claims is
// class-level" (ADR 0012). It fails closed toward the least-promotable class:
//
//   - no backing classes                    → SubjectClassUnknown
//   - ANY individual belief                  → SubjectClassIndividual
//   - any unknown belief (and no individual) → SubjectClassUnknown
//   - every belief class-level               → SubjectClassClass
//
// So a schema is class-level only when EVERY backing belief is positively
// class-level; a single individual or unclassified belief keeps it out of the
// global brain.
func AggregateSubjectClass(classes []SubjectClass) SubjectClass {
	if len(classes) == 0 {
		return SubjectClassUnknown
	}
	sawUnknown := false
	for _, c := range classes {
		switch c.Normalized() {
		case SubjectClassIndividual:
			return SubjectClassIndividual
		case SubjectClassUnknown:
			sawUnknown = true
		}
	}
	if sawUnknown {
		return SubjectClassUnknown
	}
	return SubjectClassClass
}

// EligibleForPromotion reports whether a subject class may EVER be promoted to
// the shared global brain (the ADR 0012 eligibility gate, applied before any
// corroboration counting). Only class-level subjects are eligible; individual
// and unknown fail closed.
func EligibleForPromotion(c SubjectClass) bool {
	return c.Normalized() == SubjectClassClass
}
