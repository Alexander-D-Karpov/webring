package models

import "regexp"

// SlugPattern is the shape a slug submitted through the public form must take.
var SlugPattern = regexp.MustCompile(`^[a-z0-9-]{3,50}$`)

// AdminSlugPattern is the laxer shape an admin may set from the dashboard. It also
// allows a bare number, which several long-standing members rely on.
var AdminSlugPattern = regexp.MustCompile(`^(?:[a-z0-9-]{3,50}|\d+)$`)

// reservedSlugs are the paths the ring serves itself.
//
// Member sites are reached at /{slug}, and the router matches these first, so a member
// who claimed one of these names would find their own redirect shadowed by the ring's
// own page. Refusing them at submission is the only place this can be caught: by the
// time the route is registered it is too late to complain.
var reservedSlugs = map[string]bool{
	"health": true,
	"tiers":  true,
	"submit": true,
	"login":  true,
	"logout": true,
	"admin":  true,
	"user":   true,
	"api":    true,
	"sites":  true,
	"static": true,
	"media":  true,
	"auth":   true,
	"docs":   true,
}

// ValidSlug reports whether a slug submitted through the public form is well-formed and
// not one the ring has taken.
func ValidSlug(slug string) bool {
	return SlugPattern.MatchString(slug) && !reservedSlugs[slug]
}

// ValidAdminSlug reports the same for a slug an admin sets from the dashboard, where the
// rules have always been laxer.
func ValidAdminSlug(slug string) bool {
	return AdminSlugPattern.MatchString(slug) && !reservedSlugs[slug]
}

// ReservedSlug reports whether a slug collides with a path the ring serves itself.
func ReservedSlug(slug string) bool { return reservedSlugs[slug] }
