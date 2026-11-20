package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/the-maldridge/webapp"
	"github.com/the-maldridge/webapp/db"
	"gorm.io/gorm"

	"github.com/the-maldridge/awardcast/pkg/types"
)

const (
	colorThemeListTemplate   = "@awardcast/views/colors/list.p2"
	colorThemeFormTemplate   = "@awardcast/views/colors/single.p2"
	colorThemeDeleteTemplate = "@awardcast/views/colors/delete.p2"

	// Form fields, entity-prefixed like every other form in the
	// application. The entity is a color theme and the section is
	// /colors, so the prefix is color_.
	colorNameField        = "color_name"
	colorBgField          = "color_bg"
	colorBgToField        = "color_bg_to"
	colorBgDirField       = "color_bg_dir"
	colorGradientOffField = "color_gradient_off"
	colorTextField        = "color_text"
	colorWinnerField      = "color_winner"
	colorDefaultField     = "color_default"

	// classicBg and classicInk are the presenter's original screen: ivory
	// behind black. They seed the default theme on a fresh database, and a
	// new theme form opens on them, so a venue that never edits a color
	// sees exactly the projector it always saw.
	classicBg  = "#fffff0"
	classicInk = "#000000"

	// minContrast is WCAG's threshold for body-size text, and the back row
	// of the room is what makes it the number rather than a suggestion.
	minContrast = 4.5

	// defaultGradientDirection is the direction select's opening choice,
	// and what a solid theme's empty direction displays as.
	defaultGradientDirection = "to bottom"
)

// hexColorPattern is the one color spelling the form, the database, and CSS
// agree on: a hash and six lowercase hex digits. Uppercase is normalized
// before the match rather than accepted by it, so no downstream comparison
// ever has to know that #FFFFFF is a second spelling of a color it holds.
var hexColorPattern = regexp.MustCompile(`^#[0-9a-f]{6}$`)

// gradientDirections are the eight CSS keywords this application paints,
// named clockwise from straight up. They are stored lowercase and matched
// exactly, because they are handed to CSS verbatim and a fourth spelling of
// "to bottom" is one CSS parse error away from an unpainted background.
var gradientDirections = []string{
	"to top",
	"to top right",
	"to right",
	"to bottom right",
	"to bottom",
	"to bottom left",
	"to left",
	"to top left",
}

// colorThemeRow is one theme plus the tally the list prints beside it: how
// many awards currently wear it. The tally is a pass over the awards rather
// than a group-by because a venue's award list is a dozen or two rows, and
// loading them is cheaper than a query nobody can read at a glance.
type colorThemeRow struct {
	types.ColorTheme

	UsedBy int
}

func (u *ui) uiViewColorThemeList(w http.ResponseWriter, r *http.Request) error {
	// Ordered by name because name is the unique column the list leads
	// with, and an unORDERed SELECT only happens to come back in insertion
	// order. Name is unique, so the order is total.
	themes, err := db.G[types.ColorTheme](u.DB).Order("name").Find(r.Context())
	if err != nil {
		return fmt.Errorf("listing color themes: %w", err)
	}
	awards, err := db.G[types.Award](u.DB).Find(r.Context())
	if err != nil {
		return fmt.Errorf("counting the awards per color theme: %w", err)
	}

	used := map[uint]int{}
	for _, award := range awards {
		used[award.ColorThemeID]++
	}

	rows := make([]colorThemeRow, 0, len(themes))
	for _, theme := range themes {
		rows = append(rows, colorThemeRow{ColorTheme: theme, UsedBy: used[theme.ID]})
	}

	u.View(w, r, colorThemeListTemplate, webapp.Context{"themes": rows})
	return nil
}

// uiViewColorThemeFormSingle serves the add form and the edit form, which are
// the same page the way every other entity here does: no id in the path means
// new, a malformed one is a 400, an unknown one is a 404.
func (u *ui) uiViewColorThemeFormSingle(w http.ResponseWriter, r *http.Request) error {
	id, err := formID(r)
	if err != nil {
		return err
	}

	theme := types.ColorTheme{}
	if id != 0 {
		if theme, err = oneColorTheme(r.Context(), u.DB, id); err != nil {
			return err
		}
	} else {
		theme = types.ColorTheme{Bg: classicBg, Text: classicInk, Winner: classicInk}
	}

	u.View(w, r, colorThemeFormTemplate, webapp.Context{
		"theme":      colorThemeForForm(theme),
		"directions": gradientDirections,
	})
	return nil
}

// uiViewColorThemeUpsert writes a theme from either form. The validator gets
// the whole record before anything is written, because the solid-versus-
// gradient decision spans three fields and a half-validated row would put a
// background on the projector that no reading of the form asked for.
func (u *ui) uiViewColorThemeUpsert(w http.ResponseWriter, r *http.Request) error {
	id, err := formID(r)
	if err != nil {
		return err
	}

	name, err := webapp.FormString(r, colorNameField)
	if err != nil {
		return err
	}
	bg, err := webapp.FormString(r, colorBgField)
	if err != nil {
		return err
	}
	bgTo, err := webapp.FormString(r, colorBgToField)
	if err != nil {
		return err
	}
	bgDir, err := webapp.FormString(r, colorBgDirField)
	if err != nil {
		return err
	}
	text, err := webapp.FormString(r, colorTextField)
	if err != nil {
		return err
	}
	winner, err := webapp.FormString(r, colorWinnerField)
	if err != nil {
		return err
	}
	gradientOff, err := webapp.FormBool(r, colorGradientOffField)
	if err != nil {
		return err
	}
	isDefault, err := webapp.FormBool(r, colorDefaultField)
	if err != nil {
		return err
	}

	theme := types.ColorTheme{
		ID:        id,
		Name:      name,
		Bg:        bg,
		BgTo:      bgTo,
		BgDir:     bgDir,
		Text:      text,
		Winner:    winner,
		IsDefault: isDefault,
	}
	if gradientOff {
		// The checkbox is the form saying "the second color means nothing
		// this time", and it is load bearing: an <input type="color"> that
		// nobody touched holds black, so without it the producer who wants
		// a plain ivory background submits a gradient ending in black.
		theme.BgTo = ""
	}
	return u.saveColorTheme(w, r, theme)
}

// saveColorTheme validates one theme, refuses it with the form back if it
// will do, and otherwise makes the one-default rule true and writes the row
// inside one transaction.
func (u *ui) saveColorTheme(w http.ResponseWriter, r *http.Request, theme types.ColorTheme) error {
	reasons := validateColorTheme(&theme)

	if theme.ID != 0 && !theme.IsDefault {
		// Un-checking the default is the one edit a form must not be able
		// to make: with no default, every award that wears no theme of its
		// own would be guessing. The cure lives on another theme's form,
		// which is exactly the point.
		existing, err := oneColorTheme(r.Context(), u.DB, theme.ID)
		if err != nil {
			return err
		}
		if existing.IsDefault {
			reasons = append(reasons, "This is the default theme. Make another theme the default before unchecking this one; there is always a theme the unassigned awards need.")
		}
	}

	if len(reasons) > 0 {
		return &webapp.Rejected{
			Template: colorThemeFormTemplate,
			Values: webapp.Context{
				"theme":      colorThemeForForm(theme),
				"directions": gradientDirections,
			},
			Reasons: reasons,
		}
	}

	// Clearing the old default and setting this one are one transaction:
	// two tabs racing to be the default must end with exactly one of them
	// rather than zero, and zero is the state every later screen has to
	// have a fallback query for.
	err := db.Transaction(r.Context(), u.DB, func(tx *gorm.DB) error {
		if theme.IsDefault {
			if _, err := gorm.G[types.ColorTheme](tx).
				Where("is_default is true").
				Update(r.Context(), "is_default", false); err != nil {
				return fmt.Errorf("clearing the previous default color theme: %w", err)
			}
		}
		if err := gorm.G[types.ColorTheme](tx, db.OnConflictColumns("id")).
			Create(r.Context(), &theme); err != nil {
			return fmt.Errorf("saving color theme %q: %w", theme.Name, err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	u.SeeOther(w, r, "/colors/")
	return nil
}

func (u *ui) uiViewColorThemeDeleteConfirm(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	theme, err := oneColorTheme(r.Context(), u.DB, id)
	if err != nil {
		return err
	}

	u.View(w, r, colorThemeDeleteTemplate, webapp.Context{"theme": theme})
	return nil
}

func (u *ui) uiViewColorThemeDelete(w http.ResponseWriter, r *http.Request) error {
	id, err := pathID(r)
	if err != nil {
		return err
	}
	theme, err := oneColorTheme(r.Context(), u.DB, id)
	if err != nil {
		return err
	}

	if theme.IsDefault {
		return &webapp.Rejected{
			Template: colorThemeDeleteTemplate,
			Values:   webapp.Context{"theme": theme},
			Reasons:  []string{"This is the default theme. Make another theme the default first; the awards with no theme of their own always need one to fall back to."},
		}
	}

	// The awards wearing this theme are deliberately left where they are:
	// their stored id dangles, and a dangling id resolves to the default at
	// read time. Rewriting a dozen award rows to "whatever is left" is the
	// louder action, and this screen never promised it.
	if _, err := db.G[types.ColorTheme](u.DB).Where("id = ?", id).Delete(r.Context()); err != nil {
		return fmt.Errorf("deleting color theme %d: %w", id, err)
	}

	u.SeeOther(w, r, "/colors/")
	return nil
}

// oneColorTheme loads a theme by id, turning a missing row into the
// framework's not-found so a stale bookmark renders the 404 page.
func oneColorTheme(ctx context.Context, d *db.DB, id uint) (types.ColorTheme, error) {
	theme, err := db.G[types.ColorTheme](d).Where("id = ?", id).First(ctx)
	switch {
	case errors.Is(err, gorm.ErrRecordNotFound):
		return types.ColorTheme{}, fmt.Errorf("color theme %d: %w", id, webapp.ErrNotFound)
	case err != nil:
		return types.ColorTheme{}, fmt.Errorf("loading color theme %d: %w", id, err)
	}
	return theme, nil
}

// ensureDefaultTheme seeds the theme every unassigned award wears. It runs on
// every boot and never touches a table that already holds themes: the
// producer's own list is theirs. The colors are the ones the presenter had
// before themes existed, so a venue that ignores this feature sees the screen
// it always saw, one theme named Classic sitting in the list.
func ensureDefaultTheme(ctx context.Context, d *db.DB) error {
	count, err := db.G[types.ColorTheme](d).Count(ctx, "id")
	if err != nil {
		return fmt.Errorf("counting color themes: %w", err)
	}
	if count > 0 {
		return nil
	}

	classic := types.ColorTheme{
		Name:      "Classic",
		Bg:        classicBg,
		Text:      classicInk,
		Winner:    classicInk,
		IsDefault: true,
	}
	if err := db.G[types.ColorTheme](d).Create(ctx, &classic); err != nil {
		return fmt.Errorf("seeding the classic color theme: %w", err)
	}
	slog.Info("Seeded the default color theme", "name", classic.Name)
	return nil
}

// validateColorTheme normalizes the theme in place — a trimmed name,
// lowercase hex, and the gradient fields made to agree with each other — and
// returns a reason per thing that no amount of normalizing can talk past.
//
// This function is the only writer of the agreement between BgTo and BgDir,
// which is why the model has no mode column: there is nothing for a mode
// could disagree with.
func validateColorTheme(theme *types.ColorTheme) []string {
	var reasons []string

	theme.Name = strings.TrimSpace(theme.Name)
	if theme.Name == "" {
		reasons = append(reasons, "A theme needs a name.")
	}

	theme.Bg = normalizeHex(theme.Bg)
	theme.BgTo = normalizeHex(theme.BgTo)
	theme.BgDir = strings.TrimSpace(theme.BgDir)
	theme.Text = normalizeHex(theme.Text)
	theme.Winner = normalizeHex(theme.Winner)

	if !hexColorPattern.MatchString(theme.Bg) {
		reasons = append(reasons, "The background must be a hex color like #1a1a2e.")
	}
	if !hexColorPattern.MatchString(theme.Text) {
		reasons = append(reasons, "The award text color must be a hex color like #ffffff.")
	}
	if !hexColorPattern.MatchString(theme.Winner) {
		reasons = append(reasons, "The winner name color must be a hex color like #ffd700.")
	}
	// An empty gradient end is not a broken color; it is the solid theme,
	// and the emptiness is exactly what says solid.
	if theme.BgTo != "" && !hexColorPattern.MatchString(theme.BgTo) {
		reasons = append(reasons, "The gradient end must be a hex color like #16213e, or the theme left on a solid background.")
	}

	if theme.BgTo == "" {
		// A solid theme stores no direction, however the select was left
		// sitting. Keeping whatever word was on screen would be a fact
		// about a gradient that does not exist.
		theme.BgDir = ""
	} else if !isGradientDirection(theme.BgDir) {
		reasons = append(reasons, fmt.Sprintf("Gradient direction %q is not one of the eight this application paints.", theme.BgDir))
	}

	return append(reasons, contrastReasons(theme)...)
}

// contrastReasons reports the color pairs that would not survive the back row
// of the room: the award text and the winner name against each background the
// theme can show, plus the crossing rule a gradient needs beyond its ends.
func contrastReasons(theme *types.ColorTheme) []string {
	if !hexColorPattern.MatchString(theme.Bg) {
		// Nothing measurable to compare against, and the hex complaint
		// already names the background as the thing to fix.
		return nil
	}

	var reasons []string

	against := func(what, hex, other, where string) {
		if !hexColorPattern.MatchString(hex) {
			return
		}
		if ratio := contrastRatio(hex, other); ratio < minContrast {
			reasons = append(reasons, fmt.Sprintf(
				"%s %s contrasts by %.1f; %.1f or more is what the back of the room needs.",
				what, where, ratio, minContrast))
		}
	}

	against("The award text", theme.Text, theme.Bg, "against the background")
	against("The winner name", theme.Winner, theme.Bg, "against the background")

	if theme.BgTo == "" || !hexColorPattern.MatchString(theme.BgTo) {
		return reasons
	}
	against("The award text", theme.Text, theme.BgTo, "against the gradient end")
	against("The winner name", theme.Winner, theme.BgTo, "against the gradient end")

	// Two endpoints the text clears do not mean the text survives the
	// gradient: white above black with a mid-gray title clears both endpoint
	// ratios and still vanishes at the point where the gradient passes
	// through the title's own color. A gradient's luminance runs monotonically
	// between its endpoints, so requiring both endpoints to land on the same
	// side of the color's luminance — with every endpoint ratio already at
	// least the minimum — is what keeps the whole band readable rather than
	// just its ends.
	if hexColorPattern.MatchString(theme.Text) && crossesLuminance(theme.Text, theme.Bg, theme.BgTo) {
		reasons = append(reasons, "The gradient passes through the award text color on its way between the two backgrounds; the title disappears wherever they meet.")
	}
	if hexColorPattern.MatchString(theme.Winner) && crossesLuminance(theme.Winner, theme.Bg, theme.BgTo) {
		reasons = append(reasons, "The gradient passes through the winner name color on its way between the two backgrounds; the name disappears wherever they meet.")
	}
	return reasons
}

// crossesLuminance reports whether a gradient from one color to the other
// passes through the given color's own luminance on the way. Landing exactly
// on an endpoint is not a crossing: that pairing already fails its own ratio
// at that end.
func crossesLuminance(color, from, to string) bool {
	c := luminance(color)
	return (c-luminance(from))*(c-luminance(to)) < 0
}

// luminance is WCAG's relative luminance of a #rrggbb color: the sRGB
// channels linearized and weighted the way the standard weights them. It
// expects input that already matched hexColorPattern.
func luminance(hex string) float64 {
	var channel [3]float64
	for i := range channel {
		v, err := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		if err != nil {
			// Unreachable for input the pattern matched; a zero channel is
			// the least surprising reading of a color we cannot parse, and
			// the ratio it produces is checked the same as any other.
			v = 0
		}
		s := float64(v) / 255
		if s <= 0.03928 {
			channel[i] = s / 12.92
			continue
		}
		channel[i] = math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*channel[0] + 0.7152*channel[1] + 0.0722*channel[2]
}

// contrastRatio is WCAG's contrast between two colors: the luminances raised
// by 0.05 and divided light over dark, so an identical pair reads 1 and black
// on white reads 21.
func contrastRatio(a, b string) float64 {
	hi, lo := luminance(a), luminance(b)
	if hi < lo {
		hi, lo = lo, hi
	}
	return (hi + 0.05) / (lo + 0.05)
}

// normalizeHex lowercases and trims. The pattern is the judge; this only
// keeps #FFFFFF from being stored as a second spelling of a color the list
// already shows.
func normalizeHex(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func isGradientDirection(dir string) bool {
	for _, want := range gradientDirections {
		if dir == want {
			return true
		}
	}
	return false
}

// colorThemeForForm is what the form displays for a theme. The direction
// select always shows one of its eight options, so a solid theme — whose
// stored direction is deliberately empty — displays the default word instead
// of nothing. Saving re-decides the direction from the gradient fields, so
// the display convenience never leaks into the row.
func colorThemeForForm(theme types.ColorTheme) types.ColorTheme {
	if theme.BgDir == "" {
		theme.BgDir = defaultGradientDirection
	}
	return theme
}
