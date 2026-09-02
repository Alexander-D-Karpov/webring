package health

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"regexp"
	"strconv"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// The checker has to look like a reader rather than like a checker.
//
// It used to announce itself: every request carried "webring-ring-check" and a link to the
// ring. That is polite, and it is also an invitation. A member who wanted a better score
// only had to serve one page to that string and another to everybody else, and the check
// would faithfully measure a page no human is ever shown.
//
// So each sweep borrows an ordinary desktop Chrome identity instead, drawn with crypto/rand.
// Randomness on its own would not help — a string of noise is *more* recognizable, not
// less, because no real visitor sends it, and a member could match on the noise as easily
// as on a name. What makes this work is that every identity here is one a large share of
// the member's real audience also sends. Singling out the checker means singling out those
// readers too, which is the same amount of work as simply having a widget.
//
// The version is read off the Chromium actually doing the checking rather than written
// down here, where it would rot into a giveaway within a year, and it is spread over the
// last few releases because real audiences lag behind.

// fallbackMajor stands in when Chromium will not say what version it is. Only the shape of
// the string matters to a site, but a version far in the past is conspicuous, so this is
// worth nudging forward whenever it is noticed.
const fallbackMajor = 139

// versionsBack is how many releases behind the current one an identity may claim. Chrome
// updates itself, yet at any moment a real audience is spread across the newest release
// and the couple before it.
const versionsBack = 3

// identity is one browser the checker can pretend to be.
//
// The header and the values scripts can read back have to agree. A page that reads
// navigator.platform, or the Sec-CH-UA client hints Chrome sends alongside the header, and
// finds Windows behind a Linux User-Agent has caught the checker as surely as the old name
// did — so the platform is carried through all three rather than left to the real host.
type identity struct {
	UserAgent    string
	Platform     string // what navigator.platform reports
	HintPlatform string // Sec-CH-UA-Platform
	HintVersion  string // Sec-CH-UA-Platform-Version
	Architecture string
	Bitness      string
	Major        int
}

// platforms are the three desktop platforms Chrome overwhelmingly runs on.
//
// All three are desktop on purpose. A mobile identity would invite a site to serve its
// mobile layout, and the checker would then be measuring a different page at each of the
// four viewport sizes it renders — the very comparison the viewport sweep depends on.
var platforms = []struct {
	system       string
	platform     string
	hintPlatform string
	hintVersion  string
	architecture string
	bitness      string
}{
	{
		system:       "Windows NT 10.0; Win64; x64",
		platform:     "Win32",
		hintPlatform: "Windows",
		hintVersion:  "15.0.0",
		architecture: "x86",
		bitness:      "64",
	},
	{
		system:       "Macintosh; Intel Mac OS X 10_15_7",
		platform:     "MacIntel",
		hintPlatform: "macOS",
		hintVersion:  "14.6.1",
		architecture: "x86",
		bitness:      "64",
	},
	{
		system:       "X11; Linux x86_64",
		platform:     "Linux x86_64",
		hintPlatform: "Linux",
		hintVersion:  "",
		architecture: "x86",
		bitness:      "64",
	},
}

// randomIdentity picks a platform and a recent version, both uniformly at random.
//
// current is the major version of the Chromium doing the work; anything unusable falls
// back, so a failed version probe costs realism rather than correctness.
func randomIdentity(current int) identity {
	if current < fallbackMajor {
		current = fallbackMajor
	}

	p := platforms[randIndex(len(platforms))]
	major := current - randIndex(versionsBack)

	// Chrome froze everything after the major version at 0.0.0 in release 110. Putting
	// real-looking build numbers back would be the tell, not the disguise.
	return identity{
		UserAgent: fmt.Sprintf(
			"Mozilla/5.0 (%s) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%d.0.0.0 Safari/537.36",
			p.system, major),
		Platform:     p.platform,
		HintPlatform: p.hintPlatform,
		HintVersion:  p.hintVersion,
		Architecture: p.architecture,
		Bitness:      p.bitness,
		Major:        major,
	}
}

// apply dresses a tab in this identity. It has to run before the tab navigates, or the
// request that matters has already gone out under the real one.
func (id identity) apply() chromedp.Action {
	version := strconv.Itoa(id.Major)
	brands := []*emulation.UserAgentBrandVersion{
		// The deliberately absurd entry is real: Chrome ships a nonsense brand so that
		// sites cannot assume the list has a fixed shape. Leaving it out is a tell.
		{Brand: "Not(A:Brand", Version: "99"},
		{Brand: "Google Chrome", Version: version},
		{Brand: "Chromium", Version: version},
	}

	return emulation.SetUserAgentOverride(id.UserAgent).
		WithPlatform(id.Platform).
		WithUserAgentMetadata(&emulation.UserAgentMetadata{
			Brands:          brands,
			FullVersionList: brands,
			Platform:        id.HintPlatform,
			PlatformVersion: id.HintVersion,
			Architecture:    id.Architecture,
			Bitness:         id.Bitness,
			Model:           "",
			Mobile:          false,
		})
}

// chromeMajorPattern reads the major version out of a product string such as
// "HeadlessChrome/139.0.7258.5" or "Chrome/139.0.7258.5".
var chromeMajorPattern = regexp.MustCompile(`Chrome/(\d+)\.`)

// chromeMajor extracts the major version Chromium reports for itself, or zero if the
// string is not one it recognizes.
func chromeMajor(product string) int {
	m := chromeMajorPattern.FindStringSubmatch(product)
	if m == nil {
		return 0
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return major
}

// randIndex returns a number in [0, n) from the system's entropy source.
//
// math/rand would be predictable to anyone who could guess the seed, and the whole point
// of choosing at random is that a member cannot work out in advance which identity is
// coming. A failing entropy source falls back to the first choice rather than to a panic:
// a less varied checker is better than no checker.
func randIndex(n int) int {
	if n <= 1 {
		return 0
	}
	i, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(i.Int64())
}
