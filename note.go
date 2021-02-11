package notes

import (
	"bufio"
	"bytes"
	"fmt"
	"github.com/pkg/errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	reTitleBar = regexp.MustCompile("^# (.+)$")
	// Match to horizontal ruler of markdown: https://spec.commonmark.org/0.28/#thematic-break
	// such as:
	// ---
	// ***
	// ___
	//   -	-   - -  -
	reHorizontalRule = regexp.MustCompile(`^\s{0,3}(?:(?:-+\s*){3,}|(?:\*+\s*){3,}|(?:_+\s*){3,})$`)
	closingComment   = []byte("-->\n")
)

// MismatchCategoryError represents an error caused when a user specifies mismatched category
type MismatchCategoryError struct {
	cat, pathcat, path string
}

func (e *MismatchCategoryError) Error() string {
	return fmt.Sprintf("Category does not match to file path. Category is '%s' but it should be '%s' from its file path. File path is '%s'", e.cat, e.pathcat, e.path)
}

// Is returns if given error is a MismatchCategoryError or not
func (e *MismatchCategoryError) Is(target error) bool {
	_, ok := target.(*MismatchCategoryError)
	return ok
}

// Note represents a note stored on filesystem or will be created
type Note struct {
	// Config is a configuration of notes command which was created by NewConfig()
	Config *Config
	// Category is a category string. It must not be empty
	Category string
	// Tags is tags of note. It can be empty and cannot contain comma
	Tags []string
	// Created is a datetime when note was created
	Created time.Time
	// File is a file name of the note
	File string
	// Title is a title string of the note. When the note is not created yet, it may be empty
	Title string
}

// DirPath returns the absolute category directory path of the note
func (note *Note) DirPath() string {
	return filepath.Join(note.Config.HomePath, filepath.FromSlash(note.Category))
}

// FilePath returns the absolute file path of the note
func (note *Note) FilePath() string {
	return filepath.Join(note.Config.HomePath, filepath.FromSlash(note.Category), note.File)
}

// RelFilePath returns the relative file path of the note from home directory
func (note *Note) RelFilePath() string {
	return filepath.Join(filepath.FromSlash(note.Category), note.File)
}

// TemplatePath resolves a path to template file of the note. If no template is found, it returns
// false as second return value
func (note *Note) TemplatePath() (string, bool) {
	p := note.DirPath()
	for {
		f := filepath.Join(p, ".template.md")
		if s, err := os.Stat(f); err == nil && !s.IsDir() {
			return f, true
		}
		if p == note.Config.HomePath {
			return "", false
		}
		p = filepath.Dir(p)
	}
}

// Create creates a file of the note. When title is empty, file name omitting file extension is used
// for it. This function will fail when the file is already existing.
func (note *Note) Create() error {
	var template []byte
	if p, ok := note.TemplatePath(); ok {
		b, err := ioutil.ReadFile(p)
		if err != nil {
			return errors.Wrapf(err, "Cannot read template file %q", p)
		}
		template = b
	}

	var b bytes.Buffer

	// Write metadata
        b.WriteString("---\n");
	fmt.Fprintf(&b, "- Category: %s\n", note.Category)
	fmt.Fprintf(&b, "- Tags: %s\n", strings.Join(note.Tags, ", "))
	fmt.Fprintf(&b, "- Created: %s\n", note.Created.Format(time.RFC3339))
        b.WriteString("---\n\n");

	// Write title
	title := note.Title
	if title == "" {
		title = strings.TrimSuffix(note.File, filepath.Ext(note.File))
	}
	b.WriteString("# " + title + "\n")

	if len(template) > 0 {
		b.Write(template)
	} else {
		// When template is not inserted, it's better to separate metadata and body with empty line
		b.WriteRune('\n')
	}

	d := note.DirPath()
	if err := os.MkdirAll(d, 0755); err != nil {
		return errors.Wrapf(err, "Could not create category directory '%s'", d)
	}

	p := filepath.Join(d, note.File)
	if _, err := os.Stat(p); err == nil {
		return errors.Errorf("Cannot create new note since file '%s' already exists. Please edit it", note.RelFilePath())
	}

	f, err := os.Create(p)
	if err != nil {
		return errors.Wrap(err, "Cannot create note file")
	}
	defer f.Close()

	f.Write(b.Bytes())

	return nil
}

// Open opens the note using an editor command user set. When user did not set any editor command
// with $NOTES_CLI_EDITOR, this method fails. Otherwise, an editor process is spawned with argument
// of path to the note file
func (note *Note) Open() error {
	return openEditor(note.Config, note.FilePath())
}

// ReadBodyLines reads body of note until maxLines lines and returns it as string and number of lines as int
func (note *Note) ReadBodyLines(maxLines int) (string, int, error) {
	path := note.FilePath()
	f, err := os.Open(path)
	if err != nil {
		return "", 0, errors.Wrap(err, "Cannot open note file")
	}
	defer f.Close()

	// Skip metadata
	r := bufio.NewReader(f)
	sawCat, sawTags, sawCreated := false, false, false
	for {
		t, err := r.ReadString('\n')
                if reHorizontalRule.MatchString(t) {
                        continue
                } else if strings.HasPrefix(t, "- Category: ") {
			sawCat = true
		} else if strings.HasPrefix(t, "- Tags:") {
			sawTags = true
		} else if strings.HasPrefix(t, "- Created: ") {
			sawCreated = true
		}
		if sawCat && sawTags && sawCreated {
			break
		}
		if err != nil {
			return "", 0, errors.Wrapf(err, "Cannot read metadata of note file. Some metadata may be missing in '%s'", note.RelFilePath())
		}
	}

	var buf bytes.Buffer
	var readLines int

	// Skip empty lines
	for {
		b, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		if len(b) > 1 && !reHorizontalRule.Match(b) && !bytes.Equal(b, closingComment) {
			buf.Write(b)
			readLines++
			break
		}
	}

	for numLines := 1; numLines < maxLines; numLines++ {
		b, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		readLines++
		buf.Write(b)
	}

	return buf.String(), readLines, nil
}

// NewNote creates a new note instance with given parameters and configuration. Category and file name
// cannot be empty. If given file name lacks file extension, it automatically adds ".md" to file name.
func NewNote(cat, tags, file, title string, cfg *Config) (*Note, error) {
	cat = strings.TrimSpace(cat)
	file = strings.TrimSpace(file)
	title = strings.TrimSpace(title)

	for _, part := range strings.Split(cat, "/") {
		if err := validateDirname(part); err != nil {
			return nil, errors.Wrapf(err, "Invalid category part '%s' as directory name", part)
		}
	}

	if file == "" || strings.HasPrefix(file, ".") {
		return nil, errors.New("File name cannot be empty and cannot start with '.'")
	}

	ts := []string{}
	for _, t := range strings.Split(tags, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			ts = append(ts, t)
		}
	}

	if !strings.HasSuffix(file, ".md") {
		file += ".md"
	}
	return &Note{cfg, cat, ts, time.Now(), file, title}, nil
}

// LoadNote reads note file from given path, parses it and creates Note instance. When given file path
// does not exist or when the file does note contain mandatory metadata ('Category', 'Tags' and 'Created'),
// this function returns an error
func LoadNote(path string, cfg *Config) (*Note, error) {
	// This is necessary for macOS, where path contains NFD format
	path = normPathNFD(path)

	f, err := os.Open(path)
	if err != nil {
		return nil, errors.Wrap(err, "Cannot open note file")
	}
	defer f.Close()

	note := &Note{Config: cfg}

	note.File = filepath.Base(path)

	s := bufio.NewScanner(f)
	titleFound := false
	for s.Scan() {
		line := s.Text()
		if reTitleBar.MatchString(line) {
                        note.Title = reTitleBar.FindStringSubmatch(line)[1]
			titleFound = true
		} else if strings.HasPrefix(line, "- Category: ") {
			note.Category = strings.TrimSpace(line[12:])
		} else if strings.HasPrefix(line, "- Tags:") {
			tags := strings.Split(line[7:], ",")
			note.Tags = make([]string, 0, len(tags))
			for _, t := range tags {
				t = strings.TrimSpace(t)
				if t != "" {
					note.Tags = append(note.Tags, t)
				}
			}
		} else if strings.HasPrefix(line, "- Created: ") {
			t, err := time.Parse(time.RFC3339, strings.TrimSpace(line[11:]))
			if err != nil {
				return nil, errors.Wrapf(err, "Cannot parse created date time as RFC3339 format: %s", line)
			}
			note.Created = t
                }
		if note.Category != "" && note.Tags != nil && !note.Created.IsZero() && note.Title != "" {
			break
		}
	}
	if err := s.Err(); err != nil {
		return nil, errors.Wrapf(err, "Cannot read note file '%s'", canonPath(path))
	}

	if !titleFound {
		return nil, errors.Errorf("No title found in note '%s'. Didn't you use '# ' prefix for h1 title?", canonPath(path))
	}

	if note.Category == "" || note.Tags == nil || note.Created.IsZero() {
		return nil, errors.Errorf("Missing metadata in file '%s'. 'Category', 'Tags', 'Created' are mandatory", canonPath(path));
	}

	parent := filepath.Dir(path)
	rel, err := filepath.Rel(cfg.HomePath, parent)
	name := filepath.ToSlash(rel)
	if err != nil || filepath.ToSlash(rel) != note.Category {
		return note, &MismatchCategoryError{note.Category, name, path}
	}

	return note, nil
}
