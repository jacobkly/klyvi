package imports

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

// maxExportBytes caps how much a Trakt export may unpack to. The upload is
// capped at maxImportBytes, but a ZIP can unpack to far more than it weighs.
// A heavy lifetime export unpacks to tens of megabytes.
const maxExportBytes = 256 << 20

// ErrExportTooLarge refuses a ZIP that unpacks to more than maxExportBytes.
var ErrExportTooLarge = fmt.Errorf("export unpacks to more than %d MB", maxExportBytes>>20)

// maxEntryBytes caps one entry of an export file. A real one is a few
// hundred bytes, or tens of kilobytes for a long show in a watched list.
const maxEntryBytes = 4 << 20

var errEntryTooLarge = fmt.Errorf("an entry in the export is over %d MB", maxEntryBytes>>20)

// isZIP reports whether data starts like a ZIP file.
func isZIP(data []byte) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04"))
}

// parseTraktExport reads the JSON files of the ZIP Trakt exports from
// Settings, Data, unpacking at most limit bytes in all. Files that are not
// lists of watches or ratings (the profile, lists, comments) add nothing.
func parseTraktExport(data []byte, limit int64) ([]Row, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open ZIP: %w", err)
	}

	acc := newTraktRollup()
	budget := &budgetReader{left: limit}
	var jsonFiles int
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.EqualFold(path.Ext(f.Name), ".json") {
			continue
		}
		jsonFiles++
		if err := readZIPFile(f, budget, acc); err != nil {
			return nil, err
		}
	}
	if jsonFiles == 0 {
		return nil, errors.New("this ZIP has no Trakt JSON in it. Upload the export from Trakt's Settings, Data page")
	}
	return acc.rows(), nil
}

func readZIPFile(f *zip.File, budget *budgetReader, acc *traktRollup) error {
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open %s: %w", f.Name, err)
	}
	defer rc.Close()
	budget.r = rc
	if err := acc.read(budget); err != nil {
		return fmt.Errorf("read %s: %w", f.Name, err)
	}
	return nil
}

// parseTraktJSON reads one JSON file from a Trakt export, uploaded on its
// own.
func parseTraktJSON(data []byte) ([]Row, error) {
	acc := newTraktRollup()
	if err := acc.read(bytes.NewReader(data)); err != nil {
		return nil, err
	}
	return acc.rows(), nil
}

// budgetReader reads from r while the shared budget lasts, and fails with
// ErrExportTooLarge once more than the budget has been read.
type budgetReader struct {
	r    io.Reader
	left int64
}

func (b *budgetReader) Read(p []byte) (int, error) {
	// One byte past the budget is enough to know it was exceeded.
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return n, ErrExportTooLarge
	}
	return n, err
}

// compactJSON passes JSON through with each run of whitespace outside a
// string cut to one space, and refuses an entry of the top-level list over
// maxEntryBytes. json.Decoder keeps a run of whitespace in its buffer and
// rescans it on every read, and holds a whole entry while decoding it, so
// without this a crafted file could cost as much time and memory as it
// likes.
type compactJSON struct {
	r        io.Reader
	inString bool
	escaped  bool
	space    bool // the last byte passed on was whitespace
	depth    int
	entry    int // bytes passed on since the top-level list's last comma
}

func newCompactJSON(r io.Reader) *compactJSON {
	return &compactJSON{r: r}
}

func (c *compactJSON) Read(p []byte) (int, error) {
	for {
		n, err := c.r.Read(p)
		out := 0
		for _, b := range p[:n] {
			b, keep, stepErr := c.step(b)
			if stepErr != nil {
				return out, stepErr
			}
			if keep {
				p[out] = b
				out++
			}
		}
		// A read that was all dropped whitespace reads again, rather than
		// handing back nothing.
		if out > 0 || err != nil || len(p) == 0 {
			return out, err
		}
	}
}

// step returns the byte to pass on for b, if any.
func (c *compactJSON) step(b byte) (byte, bool, error) {
	space := false
	switch {
	case c.inString:
		switch {
		case c.escaped:
			c.escaped = false
		case b == '\\':
			c.escaped = true
		case b == '"':
			c.inString = false
		}
	case b == ' ', b == '\t', b == '\n', b == '\r':
		if c.space {
			return 0, false, nil
		}
		space = true
		b = ' '
	case b == '"':
		c.inString = true
	case b == '[', b == '{':
		c.depth++
	case b == ']', b == '}':
		c.depth--
	}
	c.space = space

	if c.depth <= 1 && !c.inString && strings.IndexByte("[]{},", b) >= 0 {
		c.entry = 0
		return b, true, nil
	}
	c.entry++
	if c.entry > maxEntryBytes {
		return 0, false, errEntryTooLarge
	}
	return b, true, nil
}

// traktTitle is a film or show as Trakt describes one. A null year or TMDB
// id decodes as 0.
type traktTitle struct {
	Title string `json:"title"`
	Year  int    `json:"year"`
	IDs   struct {
		TMDB int `json:"tmdb"`
	} `json:"ids"`
}

// traktEntry is one item from any file of the export. History and ratings
// name their kind in type. The watched lists that API backups save do not,
// so the kind is read from which title the entry carries.
type traktEntry struct {
	Type          string      `json:"type"`
	WatchedAt     *string     `json:"watched_at"`
	LastWatchedAt *string     `json:"last_watched_at"`
	Rating        *float64    `json:"rating"`
	Movie         *traktTitle `json:"movie"`
	Show          *traktTitle `json:"show"`
	Season        *struct {
		Number int `json:"number"`
	} `json:"season"`
	Episode *struct {
		Season int `json:"season"`
		Number int `json:"number"`
	} `json:"episode"`
	Seasons []struct {
		Number   int `json:"number"`
		Episodes []struct {
			Number int `json:"number"`
		} `json:"episodes"`
	} `json:"seasons"`
}

func (e traktEntry) kind() string {
	if e.Type != "" {
		return e.Type
	}
	switch {
	case e.Episode != nil:
		return "episode"
	case e.Season != nil:
		return "season"
	case e.Show != nil:
		return "show"
	case e.Movie != nil:
		return "movie"
	default:
		return ""
	}
}

// traktSeason is one season's roll-up: its row and the distinct episodes
// watched, so a rewatch counts once.
type traktSeason struct {
	row      Row
	episodes map[int]bool
}

// traktRollup folds watches and ratings into one row per film, per rated
// show and per season.
type traktRollup struct {
	films   map[string]*Row
	shows   map[string]*Row
	seasons map[string]*traktSeason
}

func newTraktRollup() *traktRollup {
	return &traktRollup{
		films:   map[string]*Row{},
		shows:   map[string]*Row{},
		seasons: map[string]*traktSeason{},
	}
}

// read folds in one file. A file that is not a list is skipped, and so is
// an entry whose fields have the wrong types. Reading stops once the
// roll-up holds more rows than an import may.
func (a *traktRollup) read(r io.Reader) error {
	dec := json.NewDecoder(newCompactJSON(r))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return nil
	}
	for dec.More() {
		var e traktEntry
		if err := dec.Decode(&e); err != nil {
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) {
				continue
			}
			return err
		}
		a.add(e)
		if len(a.films)+len(a.shows)+len(a.seasons) > maxImportRows {
			return ErrTooManyRows
		}
	}
	// More reports false on a read error too, so reading the closing bracket
	// is what surfaces a truncated file or a spent budget.
	_, err = dec.Token()
	return err
}

func (a *traktRollup) add(e traktEntry) {
	rated := e.Rating != nil
	watched := e.WatchedAt != nil || e.LastWatchedAt != nil
	if !rated && !watched {
		return // a watchlist or list item: not something watched
	}

	switch e.kind() {
	case "movie":
		film := a.film(e.Movie)
		if film != nil && rated {
			film.Score = traktScore(*e.Rating)
		}
	case "show":
		if rated {
			if show := a.show(e.Show); show != nil {
				show.Score = traktScore(*e.Rating)
			}
		}
		for _, s := range e.Seasons {
			if season := a.season(e.Show, s.Number); season != nil {
				for _, ep := range s.Episodes {
					season.episodes[ep.Number] = true
				}
			}
		}
	case "season":
		if rated && e.Season != nil {
			if season := a.season(e.Show, e.Season.Number); season != nil {
				season.row.Score = traktScore(*e.Rating)
			}
		}
	case "episode":
		// An episode rating has no place to land. Klyvi rates seasons.
		if watched && e.Episode != nil {
			if season := a.season(e.Show, e.Episode.Season); season != nil {
				season.episodes[e.Episode.Number] = true
			}
		}
	}
}

// titleKey identifies a title by its TMDB id, or by its title and year when
// the export has no id for it.
func titleKey(t *traktTitle) string {
	if t.IDs.TMDB > 0 {
		return fmt.Sprintf("id:%d", t.IDs.TMDB)
	}
	return fmt.Sprintf("t:%s|%d", strings.ToLower(strings.TrimSpace(t.Title)), t.Year)
}

func titleRow(t *traktTitle, typ string) Row {
	return Row{Title: strings.TrimSpace(t.Title), Year: t.Year, Type: typ, TMDBID: t.IDs.TMDB}
}

// film returns the row for a film, adding it on first sight. A title with
// no name and no id cannot be matched, so it gets none.
func (a *traktRollup) film(t *traktTitle) *Row {
	if t == nil || (strings.TrimSpace(t.Title) == "" && t.IDs.TMDB <= 0) {
		return nil
	}
	key := titleKey(t)
	if r, ok := a.films[key]; ok {
		return r
	}
	r := titleRow(t, "movie")
	a.films[key] = &r
	return &r
}

func (a *traktRollup) show(t *traktTitle) *Row {
	if t == nil || (strings.TrimSpace(t.Title) == "" && t.IDs.TMDB <= 0) {
		return nil
	}
	key := titleKey(t)
	if r, ok := a.shows[key]; ok {
		return r
	}
	r := titleRow(t, "tv")
	a.shows[key] = &r
	return &r
}

// season returns the roll-up for one season of a show. Specials (season 0)
// are left out: they are extras, not a season to track.
func (a *traktRollup) season(t *traktTitle, number int) *traktSeason {
	if t == nil || number <= 0 || (strings.TrimSpace(t.Title) == "" && t.IDs.TMDB <= 0) {
		return nil
	}
	key := fmt.Sprintf("%s|s%d", titleKey(t), number)
	if s, ok := a.seasons[key]; ok {
		return s
	}
	row := titleRow(t, "tv")
	row.Season = number
	s := &traktSeason{row: row, episodes: map[int]bool{}}
	a.seasons[key] = s
	return s
}

// rows lists the roll-up in a stable order: films, then shows with each
// show's own rating before its seasons.
func (a *traktRollup) rows() []Row {
	films := make([]Row, 0, len(a.films))
	for _, r := range a.films {
		films = append(films, *r)
	}
	shows := make([]Row, 0, len(a.shows)+len(a.seasons))
	for _, r := range a.shows {
		shows = append(shows, *r)
	}
	for _, s := range a.seasons {
		r := s.row
		r.Watched = len(s.episodes)
		shows = append(shows, r)
	}
	sortRows(films)
	sortRows(shows)
	return append(films, shows...)
}

func sortRows(rows []Row) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if ta, tb := strings.ToLower(a.Title), strings.ToLower(b.Title); ta != tb {
			return ta < tb
		}
		if a.Year != b.Year {
			return a.Year < b.Year
		}
		if a.TMDBID != b.TMDBID {
			return a.TMDBID < b.TMDBID
		}
		return a.Season < b.Season
	})
}

// traktScore maps a Trakt rating (1 to 10) to the tracker's 0 to 100.
func traktScore(rating float64) *int {
	v := clampScore(rating * 10)
	return &v
}
