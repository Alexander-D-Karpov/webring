package models

import "testing"

func TestValidSlug(t *testing.T) {
	cases := []struct {
		slug  string
		valid bool
		why   string
	}{
		{slug: "example", valid: true},
		{slug: "my-site", valid: true},
		{slug: "a1b2", valid: true},
		{slug: "33", valid: false, why: "shorter than three characters"},
		{slug: "Has-Capitals", valid: false, why: "capitals are not allowed"},
		{slug: "under_score", valid: false, why: "underscores are not allowed"},
		{slug: "has space", valid: false, why: "spaces are not allowed"},
		{slug: "", valid: false, why: "empty"},
	}

	for _, tc := range cases {
		if got := ValidSlug(tc.slug); got != tc.valid {
			t.Errorf("ValidSlug(%q) = %v, want %v (%s)", tc.slug, got, tc.valid, tc.why)
		}
	}
}

// Member sites live at /{slug}, and the router matches the ring's own pages first. A
// member who claimed one of those names would find their redirect shadowed, so the name
// has to be refused at submission — there is nowhere later to catch it.
func TestReservedSlugsAreRefused(t *testing.T) {
	for _, slug := range []string{
		"health", "tiers", "submit", "login", "logout",
		"admin", "user", "api", "sites", "static", "media",
	} {
		t.Run(slug, func(t *testing.T) {
			if !SlugPattern.MatchString(slug) {
				t.Fatalf("%q is not even well-formed, so this test proves nothing", slug)
			}
			if !ReservedSlug(slug) {
				t.Errorf("%q is a path the ring serves but is not reserved", slug)
			}
			if ValidSlug(slug) {
				t.Errorf("%q was accepted as a member slug", slug)
			}
		})
	}
}

func TestOrdinaryNamesAreNotReserved(t *testing.T) {
	for _, slug := range []string{"healthy", "tier", "submitted", "userland", "mysite"} {
		if ReservedSlug(slug) {
			t.Errorf("%q was treated as reserved", slug)
		}
	}
}

// Several long-standing members have a bare number for a slug, shorter than the public
// form's three-character minimum. They were set from the dashboard, where the rules have
// always been laxer, and refusing them now would break existing members.
func TestAdminSlugsAllowShortBareNumbers(t *testing.T) {
	for _, slug := range []string{"2", "33"} {
		t.Run(slug, func(t *testing.T) {
			if ValidSlug(slug) {
				t.Errorf("%q was accepted through the public form", slug)
			}
			if !ValidAdminSlug(slug) {
				t.Errorf("%q was refused from the dashboard, breaking existing members", slug)
			}
		})
	}
}

func TestAdminSlugsAreStillSubjectToReservedNames(t *testing.T) {
	if ValidAdminSlug("health") {
		t.Errorf("an admin was allowed to claim a path the ring serves")
	}
}

func TestAdminSlugsRejectMalformedNames(t *testing.T) {
	for _, slug := range []string{"Has-Capitals", "under_score", "has space", ""} {
		if ValidAdminSlug(slug) {
			t.Errorf("%q was accepted from the dashboard", slug)
		}
	}
}
