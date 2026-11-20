package types

// Award represents an award that can be won.  This is the
// configuration of the award itself, and any images that might go
// with it.
type Award struct {
	ID uint

	Slug      string `gorm:"unique"`
	DispTitle string
	DispSub   string
	Image     []Image `gorm:"many2many:award_graphics;"`

	// ColorThemeID names the theme this award wears on the projector.
	// Zero is not the "broken" value but the ordinary one: it means the
	// award has no theme of its own and wears the default, which is why
	// no legacy row needs backfilling when the column arrives.
	ColorThemeID uint

	// ColorTheme is the resolved theme, filled in for whoever renders or
	// serializes an award. It is never stored: the id above is the fact,
	// and this is what that id means right now.
	ColorTheme *ColorTheme `gorm:"-"`
}

// A ColorTheme is a set of stage colors that can be assigned to awards.
// It is not the webapp layout theme; that one is compiled in, this one is
// typed into a form at the venue.
type ColorTheme struct {
	ID uint

	// Name is the handle the producer picks awards by, and is what the
	// list and the award form both spell out.
	Name string `gorm:"unique"`

	// Bg is the background, a solid color on its own. BgTo turns it into
	// the first stop of a gradient that runs to BgTo along BgDir, and the
	// emptiness of BgTo is the whole difference between solid and
	// gradient: there is no mode column for the two to disagree with.
	Bg    string
	BgTo  string
	BgDir string

	// Text is the color of the award title and subtitle, Winner that of
	// the name. Both are held to a readable contrast against the
	// background; see the validation in the server package.
	Text   string
	Winner string

	// IsDefault marks the one theme an award with no theme of its own
	// wears. Exactly one theme holds it at a time.
	IsDefault bool
}

// Image is used to store images within the database that go with the
// awards.  This isn't quite the correct way to do it, but is faster
// than using an object store for the image files.
type Image struct {
	ID uint

	Name string
	File []byte
}

// Recipient identifies an entity that has won an award.
type Recipient struct {
	ID uint

	Name string `gorm:"unique"`
}

// A Winning is a connection between an Award and a Recipient.
type Winning struct {
	ID uint

	Award   Award
	AwardID uint

	Recipient   Recipient
	RecipientID uint

	// Presented records that this winning has gone out to the room, which is the
	// reveal that names the winner rather than the present that only loads it. The
	// winnings list filters on it so whoever is running the ceremony sees what is
	// still to come. Clearing it is a deliberate act on purpose: clearing the
	// projector does not un-show a winner to an audience.
	Presented bool
}
