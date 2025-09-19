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

	Name string
}

// A Winning is a connection between an Award and a Recipient.
type Winning struct {
	ID uint

	Award   Award
	AwardID uint

	Recipient   Recipient
	RecipientID uint
}
